import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { ClockIcon, Cross2Icon, GearIcon, PaperPlaneIcon, StopIcon } from "@radix-ui/react-icons";
import { Card, Flex, IconButton, Text, TextArea } from "@radix-ui/themes";
import { Events } from "@wailsio/runtime";
import { Cancel, CloseControlPanel, CloseHistory, Connect, ConnectionSettings, DisableDesktopObservation, EnableDesktopObservation, HideSpeechBubble, OpenControlPanel, OpenHistory, RecentMessages, ReportStickerDelivery, SaveConnection, Send, SetDesktopObservationPrivacy } from "../bindings/fairy-desktop/coreservice.js";
import { CharacterExpressionBubble, CharacterSpeechBubble } from "./components/CharacterSpeechBubble.jsx";
import { PixelCharacter } from "./components/PixelCharacter.jsx";
import { resolveChatKeyboardAction } from "./companionViewState.mjs";
import {
  appendExpressionPart,
  expressionPartFromTurn,
  historyExpressionParts,
  markStickerUnavailable,
} from "./expressionViewState.mjs";
import { projectDesktopTurnActive } from "./turnViewState.mjs";

const FOOT_INPUT_MAX_HEIGHT = 88;
const MAX_REPORTED_STICKER_RECEIPTS = 16;

const defaultEndpoint = "http://127.0.0.1:8787";

function renderableVisual(visual) {
  if (!visual?.packId || !Array.isArray(visual.states) || !visual.frame || !visual.anchor) return null;
  return visual;
}

function isDesktopTurnAborted(turn) {
  return turn?.state === "failed"
    || turn?.state === "interrupted"
    || turn?.type === "failed"
    || turn?.type === "interrupted"
    || turn?.type === "stream.closed";
}

function CompanionSurface() {
  const [session, setSession] = useState(null);
  const [dockOpen, setDockOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [visualState, setVisualState] = useState("idle");
  const [active, setActive] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [inputFocused, setInputFocused] = useState(false);
  const formRef = useRef(null);
  const pendingVisualRef = useRef(null);

  useEffect(() => {
    let cancelled = false;
    Connect().then((next) => {
      if (cancelled) return;
      setSession(next);
    }).catch((cause) => { if (!cancelled) setError(cause?.message || "Core 未连接"); });
    return () => { cancelled = true; };
  }, []);

  useEffect(() => Events.On("desktop:turn", (event) => {
    const turn = event?.data ?? event;
    setActive((current) => projectDesktopTurnActive(current, turn));
    // Waiting / planning / responding keep the current standee; reply expression
    // is applied only when the turn completes.
    if (turn.type === "beat.ready" && turn.beat?.visualState) {
      pendingVisualRef.current = turn.beat.visualState;
    }
    if (turn.type === "completed" || turn.state === "completed") {
      const next = pendingVisualRef.current;
      pendingVisualRef.current = null;
      if (next) setVisualState(next);
      return;
    }
    if (turn.type === "failed" || turn.type === "interrupted" || turn.type === "stream.closed"
      || turn.state === "failed" || turn.state === "interrupted") {
      pendingVisualRef.current = null;
      setError(turn.message || "对话已中断");
    }
  }), []);

  useEffect(() => Events.On("desktop:history", (event) => {
    const state = event?.data ?? event;
    setHistoryOpen(state?.open === true);
  }), []);

  useLayoutEffect(() => {
    const el = formRef.current?.querySelector("textarea");
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, FOOT_INPUT_MAX_HEIGHT)}px`;
  }, [draft, session, active]);

  function toggleHistory() {
    const action = historyOpen ? CloseHistory() : OpenHistory();
    action.catch((cause) => setError(cause?.message || "无法切换历史"));
  }

  async function submit(event) {
    event.preventDefault();
    const input = draft.trim();
    if (!input || active) return;
    try {
      // Keep the standee on the current frame while Core thinks — do not flip to
      // "thinking". Clear the draft immediately so the dock does not look like a
      // half-stuck composer with stop + leftover text.
      setActive(true); setError(""); setDraft("");
      await Send(input);
    } catch (cause) { setError(cause?.message || "发送失败"); setActive(false); setVisualState("idle"); }
  }

  function handleInputKeyDown(event) {
    const action = resolveChatKeyboardAction(event.key, event.shiftKey);
    if (action === "submit") {
      event.preventDefault();
      if (session && !active && draft.trim()) submit(event);
      return;
    }
    if (action === "close") {
      event.preventDefault();
      if (historyOpen) { toggleHistory(); return; }
      event.currentTarget.blur();
    }
  }

  async function cancel() {
    try { await Cancel(); } catch (cause) { setError(cause?.message || "无法取消"); }
  }

  const visual = renderableVisual(session?.character?.appearance?.visual);
  return <main className="fairy-companion" onPointerLeave={() => setDockOpen(false)}>
    <section className="fairy-pet" aria-label="亚托莉桌面角色">
      <div className="fairy-pet__character" aria-label="拖动亚托莉">
        <div className="fairy-pet__pixel-motion" aria-hidden="true">
          {visual ? <PixelCharacter visual={visual} visualState={visual.states.some((state) => state.id === visualState) ? visualState : "idle"} onReady={() => setError("")} onError={(cause) => setError(cause?.message || "角色资源加载失败")} displayScale={1.69} /> : null}
        </div>
      </div>
      <div className={`fairy-foot-dock${dockOpen || historyOpen || inputFocused ? " is-visible" : ""}`} onPointerEnter={() => setDockOpen(true)}>
        <div className="fairy-foot-dock__shell">
          <div className="fairy-foot-dock__tools">
            <IconButton type="button" size="1" variant="ghost" color="gray" className="fairy-foot-dock__btn" aria-label="Core 设置" onClick={() => OpenControlPanel().catch((cause) => setError(cause?.message || "无法打开设置"))}><GearIcon /></IconButton>
            <IconButton type="button" size="1" variant={historyOpen ? "soft" : "ghost"} color="gray" className="fairy-foot-dock__btn" aria-label={historyOpen ? "关闭历史消息" : "历史消息"} aria-pressed={historyOpen} onClick={toggleHistory}><ClockIcon /></IconButton>
          </div>
          <form className="fairy-foot-dock__form" ref={formRef} onSubmit={submit}>
            <TextArea className="fairy-foot-dock__input" value={draft} onChange={(event) => setDraft(event.target.value)} onKeyDown={handleInputKeyDown} onFocus={() => { setDockOpen(true); setInputFocused(true); }} onBlur={() => setInputFocused(false)} rows={1} resize="none" placeholder={session ? "" : "正在连接 Core…"} aria-label="快捷消息输入" disabled={!session || active} />
            {active ? <IconButton type="button" size="1" color="tomato" variant="soft" className="fairy-foot-dock__btn" aria-label="停止回复" onClick={cancel}><StopIcon /></IconButton> : <IconButton type="submit" size="1" className="fairy-foot-dock__send" aria-label="发送消息" disabled={!draft.trim() || !session}><PaperPlaneIcon /></IconButton>}
          </form>
        </div>
      </div>
      {error ? <p className="fairy-surface-error" role="alert">{error}</p> : null}
    </section>
  </main>;
}

function HistoryMessage({ message }) {
  const parts = historyExpressionParts(message);
  if (parts.length === 0) return null;
  return (
    <article className={`fairy-history-message${message.role === "user" ? " is-user" : ""}`}>
      {parts.map((part) => {
        if (part.kind === "utterance") {
          return <p key={part.key} className="fairy-history-message__utterance">{part.text}</p>;
        }
        return (
          <div
            key={part.key}
            className="fairy-history-message__sticker"
            aria-label={`表情包：${part.description}`}
          >
            <span className="fairy-history-message__sticker-label">表情包</span>
            <span>{part.description}</span>
          </div>
        );
      })}
    </article>
  );
}

function HistorySurface() {
  const [messages, setMessages] = useState([]);
  const [status, setStatus] = useState("正在连接 Core…");

  useEffect(() => {
    let cancelled = false;
    let retry = null;
    const loadMessages = () => RecentMessages().then((next) => {
      if (cancelled) return;
      setMessages(next || []);
      setStatus("");
    }).catch((cause) => {
      if (cancelled) return;
      if (cause?.message === "Core session is not connected") {
        setStatus("正在连接 Core…");
        retry = window.setTimeout(loadMessages, 500);
        return;
      }
      setStatus(cause?.message || "无法读取历史消息");
    });
    loadMessages();
    return () => { cancelled = true; if (retry !== null) window.clearTimeout(retry); };
  }, []);

  useEffect(() => {
    const offTurn = Events.On("desktop:turn", (event) => {
      const turn = event?.data ?? event;
      if (turn.type === "completed") RecentMessages().then((next) => { setMessages(next || []); setStatus(""); }).catch(() => {});
    });
    const offSession = Events.On("desktop:session", (event) => {
      const session = event?.data ?? event;
      setMessages(session?.messages || []);
      setStatus("");
    });
    return () => { offTurn?.(); offSession?.(); };
  }, []);

  const visibleMessages = messages
    .slice(-20)
    .filter((message) => historyExpressionParts(message).length > 0);
  return (
    <main className="fairy-history-surface">
      <section className="fairy-history-layer" aria-label="有限历史消息">
        <Card className="fairy-history-card" size="1">
          <Flex className="fairy-history-card__bar" align="center" justify="between">
            <Text size="2" weight="medium">历史</Text>
            <IconButton type="button" size="1" variant="ghost" color="gray" aria-label="关闭历史" onClick={() => CloseHistory()}><Cross2Icon /></IconButton>
          </Flex>
          <div className="fairy-history-list">
            {visibleMessages.length
              ? visibleMessages.map((message) => <HistoryMessage key={message.id || `${message.role}:${message.sequence}`} message={message} />)
              : <Text size="1" color="gray">{status || "暂无最近消息"}</Text>}
          </div>
        </Card>
      </section>
    </main>
  );
}

function SettingsSurface() {
  const [endpoint, setEndpoint] = useState(defaultEndpoint);
  const [endpointKey, setEndpointKey] = useState("");
  const [token, setToken] = useState("");
  const [connectionStatus, setConnectionStatus] = useState("");
  const [observationStatus, setObservationStatus] = useState("");
  const [observationEnabled, setObservationEnabled] = useState(() => localStorage.getItem("fairy.observation.enabled") === "true");
  const [privacy, setPrivacy] = useState(() => localStorage.getItem("fairy.observation.privacy") || "normal");
  const [observationInterval, setObservationInterval] = useState(() => Number(localStorage.getItem("fairy.observation.interval") || 5));
  const [idleThreshold, setIdleThreshold] = useState(() => Number(localStorage.getItem("fairy.observation.idle") || 10));

  useEffect(() => {
    let cancelled = false;
    ConnectionSettings().then((settings) => {
      if (cancelled) return;
      setEndpoint(settings.endpoint || defaultEndpoint);
      setEndpointKey(settings.endpointKey || "");
    }).catch((cause) => {
      if (!cancelled) setConnectionStatus(cause?.message || "无法读取 Core 连接配置");
    });
    return () => { cancelled = true; };
  }, []);

  async function save(event) {
    event.preventDefault();
    try {
      const settings = await SaveConnection(endpoint, token, endpointKey);
      setEndpoint(settings.endpoint);
      setEndpointKey(settings.endpointKey);
      setToken("");
      setConnectionStatus("已保存到当前用户的本地连接文件，重启后将自动连接。");
    } catch (cause) {
      setConnectionStatus(cause?.message || "保存失败");
    }
  }
  async function applyObservation() {
    try {
      await SetDesktopObservationPrivacy(privacy);
      if (observationEnabled) await EnableDesktopObservation(observationInterval, idleThreshold);
      else await DisableDesktopObservation();
      localStorage.setItem("fairy.observation.enabled", String(observationEnabled));
      localStorage.setItem("fairy.observation.privacy", privacy);
      localStorage.setItem("fairy.observation.interval", String(observationInterval));
      localStorage.setItem("fairy.observation.idle", String(idleThreshold));
      setObservationStatus(observationEnabled ? "桌面观察已启用。" : "桌面观察已关闭。");
    } catch (cause) {
      setObservationStatus(cause?.message || "观察设置失败");
    }
  }
  return (
    <main className="cp-stage">
      <Card className="cp-shell">
        <div className="cp-drag-region" />
        <header className="cp-header">
          <div className="cp-header-copy">
            <span className="cp-eyebrow">桌面设置</span>
            <h1>Core 设置</h1>
          </div>
          <IconButton className="cp-close" type="button" size="2" variant="ghost" color="gray" aria-label="关闭设置" onClick={() => CloseControlPanel()}>
            <Cross2Icon />
          </IconButton>
        </header>

        <div className="cp-settings-scroll" data-testid="settings-scroll-region">
          <div className="cp-settings-content">
            <section className="cp-settings-section" aria-labelledby="core-connection-title">
              <div className="cp-settings-section__head">
                <div>
                  <p className="cp-settings-kicker">连接</p>
                  <h2 id="core-connection-title">Core 连接</h2>
                </div>
                <span className="cp-section-index" aria-hidden="true">01</span>
              </div>
              <p className="cp-settings-description">配置本机 Desktop 访问 Core 的地址和令牌。保存后将在下次启动时自动连接。</p>

              <form className="cp-settings-form" onSubmit={save}>
                <label className="cp-settings-field">
                  <span>Core 地址</span>
                  <input type="url" value={endpoint} onChange={(event) => setEndpoint(event.target.value)} autoComplete="url" spellCheck="false" />
                </label>
                <label className="cp-settings-field">
                  <span>访问令牌</span>
                  <input type="password" value={token} onChange={(event) => setToken(event.target.value)} placeholder="留空则保留已有令牌" autoComplete="off" />
                  <small>令牌只保存到当前用户的本地连接文件，不会在界面中回显。</small>
                </label>
                <button className="cp-primary-action" type="submit">保存连接配置</button>
                {connectionStatus ? <p className="cp-settings-status" role="status">{connectionStatus}</p> : null}
              </form>
            </section>

            <section className="cp-settings-section" aria-labelledby="desktop-observation-title">
              <div className="cp-settings-section__head">
                <div>
                  <p className="cp-settings-kicker">观察</p>
                  <h2 id="desktop-observation-title">桌面观察</h2>
                </div>
                <span className="cp-section-index" aria-hidden="true">02</span>
              </div>
              <p className="cp-settings-description">定期采样桌面状态，为主动互动提供有限环境信号。隐私状态会约束采样范围。</p>

              <div className="cp-settings-form">
                <label className="cp-toggle-row">
                  <span className="cp-toggle-copy">
                    <strong>定期观察</strong>
                    <small>{observationEnabled ? "已启用，将按下方间隔采样" : "已关闭，不会采样桌面状态"}</small>
                  </span>
                  <input className="cp-switch-input" type="checkbox" checked={observationEnabled} onChange={(event) => setObservationEnabled(event.target.checked)} />
                  <span className="cp-switch" aria-hidden="true" />
                </label>

                <label className="cp-settings-field">
                  <span>隐私状态</span>
                  <select value={privacy} onChange={(event) => setPrivacy(event.target.value)}>
                    <option value="normal">正常</option>
                    <option value="locked">已锁屏</option>
                    <option value="meeting">会议中</option>
                    <option value="do_not_disturb">勿扰</option>
                    <option value="protected">受保护</option>
                  </select>
                </label>

                <div className="cp-settings-grid">
                  <label className="cp-settings-field">
                    <span>采样间隔</span>
                    <span className="cp-number-control">
                      <input type="number" min="1" max="60" value={observationInterval} onChange={(event) => setObservationInterval(Number(event.target.value))} />
                      <span>分钟</span>
                    </span>
                  </label>
                  <label className="cp-settings-field">
                    <span>离开阈值</span>
                    <span className="cp-number-control">
                      <input type="number" min="1" max="240" value={idleThreshold} onChange={(event) => setIdleThreshold(Number(event.target.value))} />
                      <span>分钟</span>
                    </span>
                  </label>
                </div>

                <button className="cp-primary-action" type="button" onClick={applyObservation}>应用观察设置</button>
                {observationStatus ? <p className="cp-settings-status" role="status">{observationStatus}</p> : null}
              </div>
            </section>
          </div>
        </div>
      </Card>
    </main>
  );
}

function SpeechSurface() {
  const [bubble, setBubble] = useState({ visible: false, waiting: false, settled: false, turnId: "", parts: [] });
  const bubbleRef = useRef(bubble);
  const reportedStickersRef = useRef(new Set());
  bubbleRef.current = bubble;

  const hideBubble = () => {
    setBubble({ visible: false, waiting: false, settled: true, turnId: "", parts: [] });
    HideSpeechBubble().catch(() => {});
  };

  useEffect(() => {
    const off = Events.On("desktop:turn", (event) => {
      const turn = event?.data ?? event;
      if (isDesktopTurnAborted(turn)) {
        if (bubbleRef.current.parts.some((part) => part.kind === "sticker" && part.unavailable)) {
          setBubble((current) => ({ ...current, visible: true, waiting: false, settled: true }));
        } else {
          hideBubble();
        }
        return;
      }
      if (turn.type === "state_changed") {
        const waitingPhase = turn.state === "planning"
          || turn.state === "interpreting"
          || turn.state === "gathering"
          || turn.state === "responding";
        const nextTurnId = typeof turn.turnId === "string" ? turn.turnId.trim() : "";
        const newIdentifiedTurn = nextTurnId && bubbleRef.current.turnId !== nextTurnId;
        const localPendingTurn = turn.state === "planning" && !nextTurnId;
        setBubble((current) => {
          if (newIdentifiedTurn || localPendingTurn) {
            return waitingPhase
              ? { visible: true, waiting: true, settled: false, turnId: nextTurnId, parts: [] }
              : current;
          }
          if (current.parts.length > 0) {
            return { ...current, visible: true, waiting: false };
          }
          return waitingPhase
            ? { visible: true, waiting: true, settled: false, turnId: turn.turnId || "", parts: [] }
            : current;
        });
        return;
      }
      if (turn.type === "beat.ready") {
        const next = expressionPartFromTurn(turn);
        if (!next) return;
        setBubble((current) => {
          const currentParts = current.turnId && turn.turnId && current.turnId !== turn.turnId ? [] : current.parts;
          return {
            visible: true,
            waiting: false,
            settled: false,
            turnId: turn.turnId || current.turnId,
            parts: appendExpressionPart(currentParts, next),
          };
        });
        return;
      }
      if (turn.type === "completed") {
        if (bubbleRef.current.parts.length > 0) {
          setBubble((current) => ({ ...current, waiting: false, settled: true }));
          return;
        }
        hideBubble();
        return;
      }
    });
    return () => {
      off?.();
    };
  }, []);

  if (!bubble.visible) return <main className="fairy-speech-surface" />;
  const hasSticker = bubble.parts.some((part) => part.kind === "sticker");
  const text = bubble.parts.filter((part) => part.kind === "utterance").map((part) => part.text).join("\n");
  const clearBubble = () => hideBubble();
  const reportSticker = async (part, succeeded) => {
    if (!part.turnId || !part.beatId || reportedStickersRef.current.has(part.key)) return;
    reportedStickersRef.current.add(part.key);
    if (reportedStickersRef.current.size > MAX_REPORTED_STICKER_RECEIPTS) {
      const oldest = reportedStickersRef.current.values().next().value;
      reportedStickersRef.current.delete(oldest);
    }
    try {
      await ReportStickerDelivery(part.turnId, part.beatId, succeeded);
    } catch {
      setBubble((current) => ({
        ...current,
        settled: true,
        parts: markStickerUnavailable(current.parts, part.key, "无法确认表情包交付"),
      }));
    }
  };
  return <main className="fairy-speech-surface">
    {hasSticker
      ? <CharacterExpressionBubble
        parts={bubble.parts}
        settled={bubble.settled}
        onStickerLoad={(part) => reportSticker(part, true)}
        onStickerError={(part) => {
          setBubble((current) => ({ ...current, parts: markStickerUnavailable(current.parts, part.key, "表情包加载失败") }));
          reportSticker(part, false);
        }}
        onFaded={clearBubble}
      />
      : <CharacterSpeechBubble targetText={text} waiting={bubble.waiting} onFaded={clearBubble} />}
  </main>;
}

export function SurfaceApp() {
  const surface = new URLSearchParams(window.location.search).get("surface");
  if (surface === "control-panel") return <SettingsSurface />;
  if (surface === "history") return <HistorySurface />;
  if (surface === "speech") return <SpeechSurface />;
  return <CompanionSurface />;
}
