import { useCallback, useEffect, useMemo, useReducer, useState } from "react";

import {
  activateCharacter,
  cancelCompanionTurn,
  clearModelConnection,
  clearUserProfile,
  createCharacter,
  createCompanionSession,
  getModelConnectionStatus,
  getUserProfile,
  listCharacters,
  saveModelConnection,
  setUserProfile,
  submitCompanionTurn,
  updateCharacter,
} from "./companionClient.mjs";
import {
  createCompanionState,
  normalizeCompanionError,
  reduceCompanionState,
} from "./companionState.mjs";
import { CompanionPanel } from "./components/CompanionPanel.jsx";
import { SettingsPanel } from "./components/SettingsPanel.jsx";
import {
  getDesktopState,
  getHealth,
  listenToDesktopState,
  setAlwaysOnTop,
  setClickThrough,
} from "./desktopClient.js";
import { normalizeInvokeError } from "./desktopState.mjs";
import { DEFAULT_CHARACTER, describeCharacterFailure } from "./defaultCharacter.mjs";

const INITIAL_ASSET_STATE = Object.freeze({ phase: "loading", error: null });
const EMPTY_CATALOG = Object.freeze({ characters: Object.freeze([]), active: null, diagnostics: Object.freeze([]) });

function IconPin({ active }) {
  return (
    <svg aria-hidden="true" viewBox="0 0 24 24" fill="none">
      <path d="m8.7 3.8 6.9 1.8-1.5 4.1 3.1 3.1-1.6 1.6-3.1-3.1-4.1 1.5-1.8-6.9 2.1-2.1Z" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" />
      <path d="m10.5 13.5-5.8 5.8" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
      {active ? <circle cx="18" cy="5" r="2.2" fill="currentColor" /> : null}
    </svg>
  );
}

function IconGhost() {
  return (
    <svg aria-hidden="true" viewBox="0 0 24 24" fill="none">
      <path d="M5 19V9a7 7 0 0 1 14 0v10l-2.3-1.8L14.4 19l-2.4-1.8L9.6 19l-2.3-1.8L5 19Z" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" />
      <circle cx="9.5" cy="10" r="1" fill="currentColor" />
      <circle cx="14.5" cy="10" r="1" fill="currentColor" />
    </svg>
  );
}

function RuntimeStatus({ health, modelStatus, error }) {
  const ready = Boolean(health && modelStatus?.ready && !error);
  return (
    <div className={`runtime-status ${error ? "is-error" : ready ? "is-ready" : ""}`} aria-live="polite">
      <span className="runtime-status__dot" aria-hidden="true" />
      <span>
        {error
          ? `${error.code} · ${error.message}`
          : ready
            ? `${modelStatus.config.model} · ready`
            : "等待角色与模型连接"}
      </span>
    </div>
  );
}

export function App() {
  const [health, setHealth] = useState(null);
  const [desktop, setDesktop] = useState(null);
  const [desktopError, setDesktopError] = useState(null);
  const [catalog, setCatalog] = useState(EMPTY_CATALOG);
  const [profile, setProfile] = useState(null);
  const [modelStatus, setModelStatus] = useState(null);
  const [settingsError, setSettingsError] = useState(null);
  const [assetState, setAssetState] = useState(INITIAL_ASSET_STATE);
  const [pendingAction, setPendingAction] = useState(null);
  const [chatOpen, setChatOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [companion, dispatchCompanion] = useReducer(
    reduceCompanionState,
    undefined,
    createCompanionState,
  );

  useEffect(() => {
    let cancelled = false;
    Promise.all([getHealth(), getDesktopState()])
      .then(([nextHealth, nextDesktop]) => {
        if (cancelled) return;
        setHealth(nextHealth);
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
    let cancelled = false;
    let stopListening = null;
    listenToDesktopState(
      (nextDesktop) => {
        if (!cancelled) setDesktop(nextDesktop);
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
    Promise.all([listCharacters(), getUserProfile(), getModelConnectionStatus()])
      .then(async ([nextCatalog, nextProfile, nextModelStatus]) => {
        if (cancelled) return;
        setCatalog(nextCatalog);
        setProfile(nextProfile);
        setModelStatus(nextModelStatus);
        setSettingsError(null);
        if (!nextCatalog.active || !nextModelStatus.ready) {
          setSettingsOpen(true);
          return;
        }
        const session = await createCompanionSession();
        if (!cancelled) dispatchCompanion({ type: "session_created", session });
      })
      .catch((error) => {
        if (!cancelled) {
          setSettingsError(normalizeCompanionError(error));
          setSettingsOpen(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const closeSettings = useCallback(() => setSettingsOpen(false), []);
  const controlsDisabled = desktop === null || pendingAction !== null;
  const displayName = catalog.active?.name ?? DEFAULT_CHARACTER.displayName;
  const combinedError = settingsError ?? desktopError;
  const trayHint = useMemo(() => {
    if (desktop === null) return "正在确认菜单栏恢复入口…";
    return desktop.trayReady
      ? "点击穿透后，从菜单栏 FAIRY 选择「恢复交互」。"
      : "菜单栏入口尚未就绪，点击穿透会被拒绝。";
  }, [desktop]);

  async function runDesktopAction(action, operation) {
    setPendingAction(action);
    setDesktopError(null);
    try {
      setDesktop(await operation());
    } catch (error) {
      setDesktopError(normalizeInvokeError(error));
    } finally {
      setPendingAction(null);
    }
  }

  async function runSettingAction(action, operation) {
    setPendingAction(action);
    setSettingsError(null);
    try {
      return await operation();
    } catch (error) {
      setSettingsError(normalizeCompanionError(error));
      return null;
    } finally {
      setPendingAction(null);
    }
  }

  async function ensureSession() {
    if (companion.conversationId !== null) return companion.conversationId;
    const session = await createCompanionSession();
    dispatchCompanion({ type: "session_created", session });
    return session.conversationId;
  }

  async function handleSaveCharacter({ characterId, name, description }) {
    await runSettingAction("character", async () => {
      const character = characterId
        ? await updateCharacter(characterId, { name, description })
        : await createCharacter({ name, description });
      await activateCharacter(
        character.characterId,
        character.revision,
        companion.conversationId,
      );
      setCatalog(await listCharacters());
      if (modelStatus?.ready && companion.conversationId === null) {
        await ensureSession();
      }
    });
  }

  async function handleActivateCharacter(character) {
    await runSettingAction("character", async () => {
      await activateCharacter(
        character.characterId,
        character.revision,
        companion.conversationId,
      );
      setCatalog(await listCharacters());
    });
  }

  async function handleSaveProfile(preferredName) {
    await runSettingAction("profile", async () => {
      const update = await setUserProfile(
        preferredName,
        companion.conversationId,
      );
      setProfile(update.profile);
    });
  }

  async function handleClearProfile() {
    await runSettingAction("profile", async () => {
      const update = await clearUserProfile(companion.conversationId);
      setProfile(update.profile);
    });
  }

  async function handleSaveModel(input, apiKey) {
    await runSettingAction("model", async () => {
      const status = await saveModelConnection(input, apiKey);
      setModelStatus(status);
      if (status.ready) await ensureSession();
    });
  }

  async function handleClearModel() {
    await runSettingAction("model", async () => {
      setModelStatus(await clearModelConnection());
      dispatchCompanion({ type: "session_cleared" });
    });
  }

  async function handleSubmit() {
    if (!catalog.active || !modelStatus?.ready || !companion.conversationId) {
      setSettingsOpen(true);
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
    <main className="companion-stage">
      <h1 className="visually-hidden">FAIRY 桌面情感陪伴</h1>
      <div className="ambient ambient--warm" aria-hidden="true" />
      <div className="ambient ambient--sea" aria-hidden="true" />
      <div className="paper-grain" aria-hidden="true" />
      <div className="drag-handle" data-tauri-drag-region><span data-tauri-drag-region /></div>

      <section className="character-stage" aria-label={`${displayName} 角色`} data-chat-open={chatOpen}>
        <div className="control-rail" aria-label="窗口控制">
          <button
            className="control-button"
            type="button"
            aria-label={desktop?.alwaysOnTop ? "关闭窗口置顶" : "开启窗口置顶"}
            aria-pressed={desktop?.alwaysOnTop ?? false}
            disabled={controlsDisabled}
            onClick={() => desktop && void runDesktopAction("always-on-top", () => setAlwaysOnTop(!desktop.alwaysOnTop))}
          >
            <IconPin active={desktop?.alwaysOnTop ?? false} />
          </button>
          <button
            className="control-button"
            type="button"
            aria-label="开启鼠标点击穿透"
            aria-describedby="tray-recovery-hint"
            disabled={controlsDisabled}
            onClick={() => desktop && void runDesktopAction("click-through", () => setClickThrough(true))}
          >
            <IconGhost />
          </button>
        </div>

        <div className="character-orbit" data-tauri-drag-region>
          <div className="orbit-line orbit-line--outer" aria-hidden="true" />
          <div className="orbit-line orbit-line--inner" aria-hidden="true" />
          {assetState.phase !== "error" ? (
            <img
              className={`character-art character-art--${assetState.phase}`}
              src={DEFAULT_CHARACTER.assetPath}
              alt="亚托莉，白色水手服的静态全身桌面角色"
              draggable="false"
              loading="eager"
              fetchPriority="high"
              onLoad={() => setAssetState({ phase: "ready", error: null })}
              onError={markAssetFailed}
              data-tauri-drag-region
            />
          ) : (
            <div className="asset-error" role="alert">
              <span aria-hidden="true">✦</span>
              <strong>角色没有成功抵达</strong>
              <p>{assetState.error.message}</p>
            </div>
          )}
        </div>

        <div className="nameplate" data-tauri-drag-region>
          <span className="nameplate__mark" aria-hidden="true">✦</span>
          <span><strong>{displayName}</strong><small>{profile?.preferredName ? `for ${profile.preferredName}` : "desktop familiar"}</small></span>
        </div>
      </section>

      {!chatOpen ? (
        <div className="stage-footer">
          <p id="tray-recovery-hint" className="tray-hint">{trayHint}</p>
          <RuntimeStatus health={health} modelStatus={modelStatus} error={combinedError} />
        </div>
      ) : null}

      <CompanionPanel
        open={chatOpen}
        onToggle={() => setChatOpen((value) => !value)}
        onOpenSettings={() => setSettingsOpen(true)}
        character={catalog.active}
        preferredName={profile?.preferredName ?? null}
        companion={companion}
        onDraftChange={(value) => dispatchCompanion({ type: "draft_changed", value })}
        onSubmit={() => void handleSubmit()}
        onCancel={() => void handleCancel()}
      />

      <SettingsPanel
        open={settingsOpen}
        onClose={closeSettings}
        catalog={catalog}
        profile={profile}
        modelStatus={modelStatus}
        pending={pendingAction !== null}
        error={settingsError}
        onSaveCharacter={handleSaveCharacter}
        onActivateCharacter={handleActivateCharacter}
        onSaveProfile={handleSaveProfile}
        onClearProfile={handleClearProfile}
        onSaveModel={handleSaveModel}
        onClearModel={handleClearModel}
      />
    </main>
  );
}
