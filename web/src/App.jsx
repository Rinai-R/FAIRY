import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { useReducedMotion } from "motion/react";

import {
  cancelWailsCompanionTurn,
  createWailsCompanionSession,
  listenWailsHarnessEvents,
  loadWailsCharacterCatalog,
  loadWailsBootstrap,
  loadWailsModelStatus,
  submitWailsCompanionTurn,
} from "./wailsClient.mjs";
import { ensureWailsRuntimeReady, isWailsRuntime } from "./runtimeEnv.mjs";
import {
  isCompanionChatViewportReady,
  resolvePixelCharacterRenderKey,
  shouldDeferPixelCharacterCommit,
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
import {
  createPixelCharacterState,
  reducePixelCharacterState,
  shouldApplyReplyVisualState,
} from "./pixelCharacterState.mjs";
import { startPetWindowDrag } from "./petDragState.mjs";
import {
  listenToConfigurationChanges,
  startCurrentWindowDrag,
} from "./windowClient.js";
import { configurationRefreshTarget } from "./windowState.mjs";

function isWailsCompanionRuntime(bootstrap) {
  return isWailsRuntime() && Boolean(bootstrap?.respondRuntimeMigrated);
}

const INITIAL_ASSET_STATE = Object.freeze({ phase: "loading", error: null });
const EMPTY_CATALOG = Object.freeze({ characters: Object.freeze([]), active: null, diagnostics: Object.freeze([]) });

export function App() {
  const [desktop, setDesktop] = useState(null);
  const [desktopError, setDesktopError] = useState(null);
  const [catalog, setCatalog] = useState(EMPTY_CATALOG);
  const [modelStatus, setModelStatus] = useState(null);
  const [wailsBootstrap, setWailsBootstrap] = useState(null);
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
  const reducedMotion = Boolean(useReducedMotion());
  const [petDragging, setPetDragging] = useState(false);
  const [companion, dispatchCompanion] = useReducer(
    reduceCompanionState,
    undefined,
    createCompanionState,
  );
  const [pixelCharacter, dispatchPixelCharacter] = useReducer(
    reducePixelCharacterState,
    undefined,
    createPixelCharacterState,
  );
  const activeAppearance = catalog.active?.appearance ?? null;
  const activeVisual = activeAppearance?.status === "assigned"
    ? activeAppearance.visual
    : null;
  const characterName = catalog.active?.name ?? null;
  const wailsCompanion = isWailsCompanionRuntime(wailsBootstrap);
  const [displayCharacter, setDisplayCharacter] = useState(null);
  const [displayVisual, setDisplayVisual] = useState(null);
  const [pixelSurfaceEpoch, setPixelSurfaceEpoch] = useState(0);
  const latestPixelTargetRef = useRef({ character: null, visual: null });
  const committedPixelKeyRef = useRef(null);
  latestPixelTargetRef.current = {
    character: catalog.active,
    visual: activeVisual,
  };

  async function loadActiveModelStatus() {
    if (!(await ensureWailsRuntimeReady())) {
      return Object.freeze({ configured: false, ready: false, config: null, error: null });
    }
    const status = await loadWailsModelStatus();
    if (!status.configured) {
      return Object.freeze({ configured: false, ready: false, config: null, error: null });
    }
    return Object.freeze({
      configured: true,
      ready: true,
      config: Object.freeze({
        protocol: status.protocol,
        endpoint: status.endpoint,
        model: status.model,
        contextWindowTokens: status.contextWindowTokens,
        authMode: status.authMode,
      }),
      error: null,
    });
  }

  async function loadActiveCharacterCatalog() {
    if (!(await ensureWailsRuntimeReady())) {
      return EMPTY_CATALOG;
    }
    return loadWailsCharacterCatalog();
  }

  useEffect(() => {
    let cancelled = false;
    loadWailsBootstrap()
      .then((status) => {
        if (!cancelled) setWailsBootstrap(status);
      })
      .catch(() => {
        if (!cancelled) setWailsBootstrap(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  function commitPixelDisplay({ remount }) {
    const { character, visual } = latestPixelTargetRef.current;
    if (character === null) {
      if (committedPixelKeyRef.current === null) return;
      committedPixelKeyRef.current = null;
      setDisplayCharacter(null);
      setDisplayVisual(null);
      setAssetState(INITIAL_ASSET_STATE);
      return;
    }
    const appearance = character.appearance ?? null;
    if (appearance?.status === "unassigned") {
      committedPixelKeyRef.current = null;
      setDisplayCharacter(character);
      setDisplayVisual(null);
      setAssetState({
        phase: "error",
        error: {
          code: "CHARACTER_APPEARANCE_UNASSIGNED",
          message: `${character.name} 尚未选择外观。`,
        },
      });
      return;
    }
    if (appearance?.status === "unavailable") {
      committedPixelKeyRef.current = null;
      setDisplayCharacter(character);
      setDisplayVisual(null);
      setAssetState({
        phase: "error",
        error: {
          code: "CHARACTER_APPEARANCE_UNAVAILABLE",
          message: `${character.name} 的外观不可用。`,
        },
      });
      return;
    }
    const nextKey = resolvePixelCharacterRenderKey(character, visual);
    if (!remount && nextKey === committedPixelKeyRef.current) {
      return;
    }
    committedPixelKeyRef.current = nextKey;
    setDisplayCharacter(character);
    setDisplayVisual(visual);
    if (remount) {
      setPixelSurfaceEpoch((epoch) => epoch + 1);
    }
    setAssetState(INITIAL_ASSET_STATE);
  }

  useEffect(() => {
    // While settings hide the companion window, keep the last committed Pixi
    // identity. Committing a character switch into a hidden WKWebView leaves a
    // blank canvas that opacity/drag cannot recover.
    if (
      desktop !== null
      && shouldDeferPixelCharacterCommit({
        desktopVisible: desktop.visible,
        controlPanelVisible: desktop.controlPanelVisible,
      })
    ) {
      return;
    }
    commitPixelDisplay({ remount: false });
  }, [
    activeAppearance?.status,
    activeAppearance?.bindingRevision,
    activeVisual?.packId,
    catalog.active?.characterId,
    catalog.active?.name,
    desktop?.visible,
    desktop?.controlPanelVisible,
  ]);

  useEffect(() => {
    dispatchPixelCharacter({
      type: "context_changed",
      context: {
        availableStates: activeVisual?.states?.map((state) => state.id) ?? ["idle"],
        chatOpen: desktop?.companionSurface === "chat",
        dragging: petDragging,
        petVisible: petVisualOpen && desktop?.visible !== false,
        reducedMotion,
        sessionState: companion.sessionState,
        settingsOpen: settingsRequested || desktop?.controlPanelVisible === true,
        submitting: companion.submitting,
      },
    });
  }, [
    activeVisual?.states,
    companion.sessionState,
    companion.submitting,
    desktop?.companionSurface,
    desktop?.controlPanelVisible,
    desktop?.visible,
    petDragging,
    petVisualOpen,
    reducedMotion,
    settingsRequested,
  ]);

  useEffect(() => {
    if (!petDragging) return undefined;
    const finishDrag = () => setPetDragging(false);
    window.addEventListener("pointerup", finishDrag, { once: true });
    window.addEventListener("blur", finishDrag, { once: true });
    return () => {
      window.removeEventListener("pointerup", finishDrag);
      window.removeEventListener("blur", finishDrag);
    };
  }, [petDragging]);

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
          ? loadActiveCharacterCatalog().then((nextCatalog) => {
            if (!cancelled) setCatalog(nextCatalog);
          })
          : loadActiveModelStatus().then((nextModelStatus) => {
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
          // Force a fresh Pixi surface after the native window is shown again.
          commitPixelDisplay({ remount: true });
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
    Promise.all([loadActiveCharacterCatalog(), loadActiveModelStatus()])
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
      activeVisual === null ||
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
    if (!wailsCompanion) return undefined;
    createWailsCompanionSession(catalog.active.characterId)
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
  }, [
    activeVisual,
    catalog.active,
    modelStatus?.ready,
    wailsCompanion,
    companion.characterId,
    companion.conversationId,
  ]);

  const controlsDisabled = desktop === null || pendingAction !== null || settingsRequested;
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

  function handlePetDragStart(event) {
    startPetWindowDrag({
      event,
      desktopReady: desktop !== null,
      petVisualOpen,
      // Wails moves the window via --wails-draggable + mousedown; consuming
      // pointerdown would suppress that native drag path.
      consumePointerEvent: false,
      startDragging: startCurrentWindowDrag,
      setDragging: setPetDragging,
      onError: (error) => {
        setDesktopError(normalizeInvokeError(error));
      },
    });
  }

  async function handleSubmit() {
    if (!catalog.active || !modelStatus?.ready || !companion.conversationId) {
      handleRequestControlPanel();
      return;
    }
    const input = companion.draft;
    dispatchCompanion({ type: "submit_started", text: input });
    try {
      if (!wailsCompanion) return;
      if (activeVisual === null) {
        throw Object.freeze({
          code: "CHARACTER_APPEARANCE_UNASSIGNED",
          message: "当前角色尚未绑定可用外观，无法提交对话。",
          retryable: false,
        });
      }
      let stopListening = () => {};
      stopListening = await listenWailsHarnessEvents(
        (event) => {
          dispatchCompanion({ type: "harness_event", event });
          if (shouldApplyReplyVisualState(event)) {
            dispatchPixelCharacter({
              type: "visual_state_changed",
              visualState: event.payload.visualState,
            });
          }
        },
        (error) => {
          stopListening();
          dispatchCompanion({ type: "invoke_failed", error });
        },
      );
      try {
        await submitWailsCompanionTurn({
          conversationId: companion.conversationId,
          input: input.trim(),
          speechEnabled: true,
        });
      } finally {
        stopListening();
      }
    } catch (error) {
      dispatchCompanion({ type: "invoke_failed", error });
    }
  }

  async function handleCancel() {
    if (!companion.activeTurnId) return;
    try {
      if (!wailsCompanion || !companion.conversationId) return;
      await cancelWailsCompanionTurn(companion.conversationId, companion.activeTurnId);
    } catch (error) {
      dispatchCompanion({ type: "invoke_failed", error });
    }
  }

  const markAssetReady = useCallback(() => {
    setAssetState({ phase: "ready", error: null });
  }, []);

  const markAssetFailed = useCallback(() => {
    setAssetState({
      phase: "error",
      error: {
        code: "CHARACTER_ASSET_FAILED",
        message: characterName
          ? `无法加载 ${characterName} 的角色图片。`
          : "无法加载当前角色图片。",
      },
    });
  }, [characterName]);

  return (
    <main
      className="fairy-companion"
      data-surface={desktop?.companionSurface ?? "idle"}
      ref={companionRoot}
    >
      <h1 className="visually-hidden">FAIRY 桌面角色对话</h1>
      <CompanionPanel
        characterName={characterName}
        character={displayCharacter}
        visual={displayVisual}
        pixelCharacter={pixelCharacter}
        assetState={assetState}
        pixelSurfaceEpoch={pixelSurfaceEpoch}
        onAssetReady={markAssetReady}
        onAssetError={markAssetFailed}
        onPetDragStart={handlePetDragStart}
        onPetDragEnd={() => setPetDragging(false)}
        petDragging={petDragging}
        historyMounted={chatPopoverMounted}
        historyVisualOpen={chatVisualOpen}
        petVisualOpen={petVisualOpen}
        controlsDisabled={controlsDisabled}
        onOpenHistory={() => void handleOpenChat()}
        onRequestCloseHistory={handleRequestCloseChat}
        onHistoryExitComplete={() => void handleChatExitComplete()}
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
