import { useEffect, useReducer, useRef, useState } from "react";

import {
  cancelCompanionTurn,
  createCompanionSession,
  getModelConnectionStatus,
  listCharacters,
  submitCompanionTurn,
} from "./companionClient.mjs";
import {
  isCompanionChatViewportReady,
  trackControlPanelReturn,
} from "./companionViewState.mjs";
import {
  createCompanionState,
  normalizeCompanionError,
  reduceCompanionState,
} from "./companionState.mjs";
import { CompanionPanel } from "./components/CompanionPanel.jsx";
import {
  closeCompanionChat,
  getDesktopState,
  listenToDesktopState,
  openCompanionChat,
  showControlPanel,
} from "./desktopClient.js";
import { normalizeInvokeError } from "./desktopState.mjs";
import { DEFAULT_CHARACTER, describeCharacterFailure } from "./defaultCharacter.mjs";
import { listenToConfigurationChanges } from "./windowClient.js";
import { configurationRefreshTarget } from "./windowState.mjs";

const INITIAL_ASSET_STATE = Object.freeze({ phase: "loading", error: null });
const EMPTY_CATALOG = Object.freeze({ characters: Object.freeze([]), active: null, diagnostics: Object.freeze([]) });

export function App() {
  const [desktop, setDesktop] = useState(null);
  const [desktopError, setDesktopError] = useState(null);
  const [catalog, setCatalog] = useState(EMPTY_CATALOG);
  const [modelStatus, setModelStatus] = useState(null);
  const [settingsError, setSettingsError] = useState(null);
  const [assetState, setAssetState] = useState(INITIAL_ASSET_STATE);
  const [pendingAction, setPendingAction] = useState(null);
  const [chatPopoverMounted, setChatPopoverMounted] = useState(false);
  const [chatVisualOpen, setChatVisualOpen] = useState(false);
  const [petVisualOpen, setPetVisualOpen] = useState(true);
  const [settingsRequested, setSettingsRequested] = useState(false);
  const settingsRequestedRef = useRef(false);
  const chatClosing = useRef(false);
  const returningFromControlPanel = useRef(false);
  const companionRoot = useRef(null);
  const sessionCreating = useRef(false);
  const [companion, dispatchCompanion] = useReducer(
    reduceCompanionState,
    undefined,
    createCompanionState,
  );

  useEffect(() => {
    let cancelled = false;
    getDesktopState()
      .then((nextDesktop) => {
        if (cancelled) return;
        setDesktop(nextDesktop);
        setDesktopError(null);
      })
      .catch((error) => {
        if (!cancelled) setDesktopError(normalizeInvokeError(error));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (desktop?.companionSurface === "idle") {
      chatClosing.current = false;
      setChatPopoverMounted(false);
      setChatVisualOpen(false);
      return undefined;
    }
    if (desktop?.companionSurface === "chat" && !chatClosing.current) {
      const root = companionRoot.current;
      if (!root) return undefined;
      const showWhenReady = () => {
        if (isCompanionChatViewportReady(root.getBoundingClientRect().width)) {
          setChatPopoverMounted(true);
          setChatVisualOpen(true);
          return true;
        }
        return false;
      };
      if (showWhenReady()) return undefined;

      const observer = new ResizeObserver(() => {
        if (showWhenReady()) observer.disconnect();
      });
      observer.observe(root);
      return () => {
        observer.disconnect();
      };
    }
    return undefined;
  }, [desktop?.companionSurface]);

  useEffect(() => {
    if (
      desktop?.phase === "transitioning_to_settings" ||
      desktop?.phase === "control_panel_visible"
    ) {
      setPetVisualOpen(false);
    }
  }, [desktop?.phase]);

  useEffect(() => {
    let cancelled = false;
    let stopListening = null;
    listenToConfigurationChanges(
      (change) => {
        const target = configurationRefreshTarget(change);
        if (target === null) {
          setSettingsError(null);
          return;
        }
        const refresh = target === "character"
          ? listCharacters().then((nextCatalog) => {
            if (!cancelled) setCatalog(nextCatalog);
          })
          : getModelConnectionStatus().then((nextModelStatus) => {
            if (!cancelled) setModelStatus(nextModelStatus);
          });
        refresh
          .then(() => {
            if (!cancelled) setSettingsError(null);
          })
          .catch((error) => {
            if (!cancelled) setSettingsError(normalizeCompanionError(error));
          });
      },
      () => {
        if (!cancelled) {
          setSettingsError({
            code: "INVALID_CONFIGURATION_EVENT",
            message: "收到无法识别的配置变更通知。",
            retryable: false,
          });
        }
      },
    )
      .then((unlisten) => {
        if (cancelled) unlisten();
        else stopListening = unlisten;
      })
      .catch((error) => {
        if (!cancelled) setSettingsError(normalizeCompanionError(error));
      });
    return () => {
      cancelled = true;
      stopListening?.();
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    let stopListening = null;
    listenToDesktopState(
      (nextDesktop) => {
        if (cancelled) return;
        const returnState = trackControlPanelReturn(
          returningFromControlPanel.current,
          nextDesktop.phase,
          nextDesktop.visible,
        );
        returningFromControlPanel.current = returnState.latched;
        if (returnState.revealPet) {
          setSettingsMode(false);
          setPetVisualOpen(true);
        }
        setDesktop(nextDesktop);
      },
      (error) => {
        if (!cancelled) setDesktopError(normalizeInvokeError(error));
      },
    )
      .then((unlisten) => {
        if (cancelled) unlisten();
        else stopListening = unlisten;
      })
      .catch((error) => {
        if (!cancelled) setDesktopError(normalizeInvokeError(error));
      });
    return () => {
      cancelled = true;
      stopListening?.();
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    Promise.all([listCharacters(), getModelConnectionStatus()])
      .then(([nextCatalog, nextModelStatus]) => {
        if (cancelled) return;
        setCatalog(nextCatalog);
        setModelStatus(nextModelStatus);
        setSettingsError(null);
      })
      .catch((error) => {
        if (!cancelled) {
          setSettingsError(normalizeCompanionError(error));
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (
      !catalog.active ||
      !modelStatus?.ready ||
      sessionCreating.current
    ) {
      return undefined;
    }
    if (
      companion.conversationId !== null &&
      companion.characterId === catalog.active.characterId
    ) {
      return undefined;
    }
    let cancelled = false;
    sessionCreating.current = true;
    createCompanionSession()
      .then((session) => {
        if (!cancelled) dispatchCompanion({ type: "session_created", session });
      })
      .catch((error) => {
        if (!cancelled) setSettingsError(normalizeCompanionError(error));
      })
      .finally(() => {
        sessionCreating.current = false;
      });
    return () => {
      cancelled = true;
    };
  }, [catalog.active, modelStatus?.ready, companion.characterId, companion.conversationId]);

  const controlsDisabled = desktop === null || pendingAction !== null || settingsRequested;
  const displayName = catalog.active?.name ?? DEFAULT_CHARACTER.displayName;

  function setSettingsMode(requested) {
    settingsRequestedRef.current = requested;
    setSettingsRequested(requested);
  }

  async function runDesktopAction(action, operation) {
    setPendingAction(action);
    setDesktopError(null);
    try {
      const nextDesktop = await operation();
      setDesktop(nextDesktop);
      return nextDesktop;
    } catch (error) {
      setDesktopError(normalizeInvokeError(error));
      return null;
    } finally {
      setPendingAction(null);
    }
  }

  async function handleOpenChat() {
    if (desktop?.companionSurface === "chat") {
      chatClosing.current = false;
      setChatPopoverMounted(true);
      setChatVisualOpen(true);
      return;
    }
    await runDesktopAction("open-chat", openCompanionChat);
  }

  function handleRequestCloseChat() {
    if (desktop?.companionSurface !== "chat" || chatClosing.current) return;
    chatClosing.current = true;
    setChatVisualOpen(false);
  }

  async function handleChatExitComplete() {
    if (!chatClosing.current || desktop?.companionSurface !== "chat") return;
    const nextDesktop = await runDesktopAction("close-chat", closeCompanionChat);
    if (nextDesktop?.companionSurface === "idle") {
      chatClosing.current = false;
      setChatPopoverMounted(false);
      if (settingsRequestedRef.current) setPetVisualOpen(false);
      return;
    }
    chatClosing.current = false;
    setChatVisualOpen(true);
  }

  function handleRequestControlPanel() {
    if (settingsRequestedRef.current || pendingAction !== null) return;
    setSettingsMode(true);
    if (desktop?.companionSurface === "chat") {
      handleRequestCloseChat();
      return;
    }
    setPetVisualOpen(false);
  }

  async function handlePetExitComplete() {
    if (!settingsRequestedRef.current || desktop?.phase !== "companion_idle") return;
    const nextDesktop = await runDesktopAction("control-panel", showControlPanel);
    if (nextDesktop?.phase !== "control_panel_visible") {
      setSettingsMode(false);
      setPetVisualOpen(true);
    }
  }

  async function handleSubmit() {
    if (!catalog.active || !modelStatus?.ready || !companion.conversationId) {
      handleRequestControlPanel();
      return;
    }
    const input = companion.draft;
    dispatchCompanion({ type: "submit_started", text: input });
    try {
      await submitCompanionTurn({
        conversationId: companion.conversationId,
        input,
        speechEnabled: false,
        onEvent: (event) => dispatchCompanion({ type: "harness_event", event }),
        onProtocolError: (error) => dispatchCompanion({ type: "invoke_failed", error }),
      });
    } catch (error) {
      dispatchCompanion({ type: "invoke_failed", error });
    }
  }

  async function handleCancel() {
    if (!companion.activeTurnId) return;
    try {
      await cancelCompanionTurn(companion.activeTurnId);
    } catch (error) {
      dispatchCompanion({ type: "invoke_failed", error });
    }
  }

  function markAssetFailed() {
    setAssetState({ phase: "error", error: describeCharacterFailure(DEFAULT_CHARACTER) });
  }

  return (
    <main
      className="fairy-companion"
      data-surface={desktop?.companionSurface ?? "idle"}
      ref={companionRoot}
    >
      <h1 className="visually-hidden">FAIRY 桌面角色对话</h1>
      <CompanionPanel
        characterName={displayName}
        character={catalog.active}
        assetPath={DEFAULT_CHARACTER.assetPath}
        assetState={assetState}
        onAssetReady={() => setAssetState({ phase: "ready", error: null })}
        onAssetError={markAssetFailed}
        popoverMounted={chatPopoverMounted}
        chatVisualOpen={chatVisualOpen}
        petVisualOpen={petVisualOpen}
        controlsDisabled={controlsDisabled}
        onOpenChat={() => void handleOpenChat()}
        onRequestCloseChat={handleRequestCloseChat}
        onChatExitComplete={() => void handleChatExitComplete()}
        onPetExitComplete={() => void handlePetExitComplete()}
        onOpenControlPanel={handleRequestControlPanel}
        companion={companion}
        onDraftChange={(value) => dispatchCompanion({ type: "draft_changed", value })}
        onSubmit={() => void handleSubmit()}
        onCancel={() => void handleCancel()}
        externalError={settingsError ?? desktopError}
      />
      <p className="visually-hidden" aria-live="polite">
        {desktopError ? `${desktopError.code}：${desktopError.message}` : ""}
      </p>
    </main>
  );
}
