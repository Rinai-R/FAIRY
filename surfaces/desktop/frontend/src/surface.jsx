import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { ClockIcon, Cross2Icon, GearIcon, PaperPlaneIcon, StopIcon } from "@radix-ui/react-icons";
import { Card, Flex, IconButton, Text, TextArea, TextField } from "@radix-ui/themes";
import { Events } from "@wailsio/runtime";
import { Cancel, CloseControlPanel, CloseHistory, Connect, HideSpeechBubble, OpenControlPanel, OpenHistory, RecentMessages, SaveConnection, Send } from "../bindings/fairy-desktop/coreservice.js";
import { CharacterSpeechBubble } from "./components/CharacterSpeechBubble.jsx";
import { PixelCharacter } from "./components/PixelCharacter.jsx";
import { resolveChatKeyboardAction } from "./companionViewState.mjs";

const FOOT_INPUT_MAX_HEIGHT = 88;

const defaultEndpoint = "http://127.0.0.1:8787";
function storedEndpoint() { return localStorage.getItem("fairy.endpoint") || defaultEndpoint; }
function storedKey() { return localStorage.getItem("fairy.endpointKey") || ""; }

function renderableVisual(visual) {
  if (!visual?.packId || !Array.isArray(visual.states) || !visual.frame || !visual.anchor) return null;
  return visual;
}

function CompanionSurface() {
  const [endpoint, setEndpoint] = useState(storedEndpoint);
  const [endpointKey, setEndpointKey] = useState(storedKey);
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
    Connect(endpoint, endpointKey).then((next) => {
      if (cancelled) return;
      setSession(next);
      setEndpointKey(next.settings.endpointKey);
      localStorage.setItem("fairy.endpoint", next.settings.endpoint);
      localStorage.setItem("fairy.endpointKey", next.settings.endpointKey);
    }).catch((cause) => { if (!cancelled) setError(cause?.message || "Core 未连接"); });
    return () => { cancelled = true; };
  }, [endpoint, endpointKey]);

  useEffect(() => Events.On("desktop:turn", (event) => {
    const turn = event?.data ?? event;
    // Waiting / planning / responding must keep the current standee (see
    // pixelCharacterState: "dialogue lifecycle keeps the current image while waiting").
    // Reply expression is applied only when the turn completes.
    if (turn.type === "beat.ready" && turn.beat?.visualState) {
      pendingVisualRef.current = turn.beat.visualState;
    }
    if (turn.type === "completed") {
      setActive(false);
      const next = pendingVisualRef.current;
      pendingVisualRef.current = null;
      if (next) setVisualState(next);
      return;
    }
    if (turn.type === "failed" || turn.type === "interrupted" || turn.type === "stream.closed") {
      setActive(false);
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
      await Send(input, false);
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

  return <main className="fairy-history-surface"><section className="fairy-history-layer" aria-label="有限历史消息"><Card className="fairy-history-card" size="1"><Flex className="fairy-history-card__bar" align="center" justify="between"><Text size="2" weight="medium">历史</Text><IconButton type="button" size="1" variant="ghost" color="gray" aria-label="关闭历史" onClick={() => CloseHistory()}><Cross2Icon /></IconButton></Flex><div className="fairy-history-list">{messages.length ? messages.slice(-20).map((message) => <p key={message.id} className={message.role === "user" ? "is-user" : ""}>{message.content}</p>) : <Text size="1" color="gray">{status || "暂无最近消息"}</Text>}</div></Card></section></main>;
}

function SettingsSurface() {
  const [endpoint, setEndpoint] = useState(storedEndpoint);
  const [endpointKey, setEndpointKey] = useState(storedKey);
  const [token, setToken] = useState("");
  const [status, setStatus] = useState("");
  async function save(event) {
    event.preventDefault();
    try {
      const settings = await SaveConnection(endpoint, token, endpointKey);
      localStorage.setItem("fairy.endpoint", settings.endpoint);
      localStorage.setItem("fairy.endpointKey", settings.endpointKey);
      setEndpoint(settings.endpoint); setEndpointKey(settings.endpointKey); setToken(""); setStatus("已保存到 macOS Keychain，并已准备重连。");
    } catch (cause) { setStatus(cause?.message || "保存失败"); }
  }
  return <main className="cp-stage"><Card className="cp-shell"><div className="cp-drag-region" /><header className="cp-header"><Text as="h1" size="3" weight="medium">Core 设置</Text><IconButton type="button" size="1" variant="ghost" color="gray" aria-label="关闭设置" onClick={() => CloseControlPanel()}><Cross2Icon /></IconButton></header><section className="cp-page"><form className="cp-form" onSubmit={save}><label className="cp-field"><Text size="1" weight="medium">Core 地址</Text><TextField.Root size="1" value={endpoint} onChange={(event) => setEndpoint(event.target.value)} /></label><label className="cp-field"><Text size="1" weight="medium">访问令牌</Text><TextField.Root size="1" type="password" value={token} onChange={(event) => setToken(event.target.value)} placeholder="留空则保留已有令牌" /></label><button className="cp-save" type="submit">保存并连接</button>{status ? <Text size="1" color="gray">{status}</Text> : null}</form></section></Card></main>;
}

function SpeechSurface() {
  const [bubble, setBubble] = useState({ visible: false, waiting: false, text: "" });
  const bubbleRef = useRef(bubble);
  bubbleRef.current = bubble;

  useEffect(() => Events.On("desktop:turn", (event) => {
    const turn = event?.data ?? event;
    // Local Send() emits planning immediately for floating dots. Later Core
    // state_changed events must NOT wipe an arrived reply back to waiting.
    if (turn.type === "state_changed") {
      const waitingPhase = turn.state === "planning"
        || turn.state === "interpreting"
        || turn.state === "gathering"
        || turn.state === "responding";
      setBubble((current) => {
        if (current.text.length > 0) {
          return { ...current, visible: true, waiting: false };
        }
        if (waitingPhase) {
          return { visible: true, waiting: true, text: "" };
        }
        return { ...current, visible: true, waiting: true };
      });
      return;
    }
    if (turn.type === "beat.ready" && turn.beat?.displayText) {
      setBubble({ visible: true, waiting: false, text: turn.beat.displayText });
      return;
    }
    if (turn.type === "completed") {
      if (bubbleRef.current.text.length > 0) {
        setBubble((current) => ({ ...current, waiting: false }));
        return;
      }
      setBubble({ visible: false, waiting: false, text: "" });
      HideSpeechBubble();
      return;
    }
    if (turn.type === "failed" || turn.type === "interrupted" || turn.type === "stream.closed") {
      setBubble({ visible: false, waiting: false, text: "" });
      HideSpeechBubble();
    }
  }), []);

  if (!bubble.visible) return <main className="fairy-speech-surface" />;
  return <main className="fairy-speech-surface"><CharacterSpeechBubble targetText={bubble.text} waiting={bubble.waiting} onFaded={() => { setBubble({ visible: false, waiting: false, text: "" }); HideSpeechBubble(); }} /></main>;
}

export function SurfaceApp() {
  const surface = new URLSearchParams(window.location.search).get("surface");
  if (surface === "control-panel") return <SettingsSurface />;
  if (surface === "history") return <HistorySurface />;
  if (surface === "speech") return <SpeechSurface />;
  return <CompanionSurface />;
}
