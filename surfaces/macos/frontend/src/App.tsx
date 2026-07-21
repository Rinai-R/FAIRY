import { useEffect, useMemo, useReducer, useState } from "react";
import { ActivityLogIcon, ChatBubbleIcon, Cross2Icon, PaperPlaneIcon } from "@radix-ui/react-icons";
import { chatReducer, initialChatState } from "./state";

declare global { interface Window { go?: { main?: { AppService?: any } }; runtime?: { EventsOn?: (name: string, cb: (value: any) => void) => (() => void) | void } } }
const service = () => window.go?.main?.AppService;
const iconLabel = (label: string) => <span className="icon" aria-hidden="true">{label}</span>;

export default function App() {
  const [state, dispatch] = useReducer(chatReducer, initialChatState);
  const [endpoint, setEndpoint] = useState(localStorage.getItem("fairy.endpoint") || "http://127.0.0.1:8787");
  const [surfaceKey, setSurfaceKey] = useState(localStorage.getItem("fairy.surfaceKey") || "");
  const [token, setToken] = useState("");
  const [showSettings, setShowSettings] = useState(false);
  const [connection, setConnection] = useState<any>(null);
  const [visualURL, setVisualURL] = useState("");
  const canUse = Boolean(service());
  const statusText = useMemo(() => state.status === "connecting" ? "连接中" : state.status === "sending" ? "回复中" : state.status === "error" ? "需要处理" : connection ? "已连接" : "未连接", [state.status, connection]);

  useEffect(() => {
    const off = window.runtime?.EventsOn?.("turn:event", (event: any) => {
      const type = event.type || event.Type;
      if (type === "beat.ready" && (event.beat || event.Beat)) dispatch({ type: "beat", beat: event.beat || event.Beat });
      else if ((event.harness?.state || event.Harness?.State) === "completed") dispatch({ type: "completed" });
      else if ((event.harness?.state || event.Harness?.State) === "interrupted") dispatch({ type: "interrupted" });
      else if ((event.harness?.state || event.Harness?.State) === "failed") dispatch({ type: "failed", message: event.failure?.message || "Core 返回失败" });
    });
    return () => { if (typeof off === "function") off(); };
  }, []);

  useEffect(() => {
    const next = connection?.visuals?.find((visual: any) => visual.id === state.visualState)?.dataUrl;
    if (!next || next === visualURL) return;
    const image = new Image();
    image.onload = () => setVisualURL(next);
    image.src = next;
  }, [connection, state.visualState, visualURL]);

  async function connect() {
    dispatch({ type: "connect" });
    try {
      const nextKey = surfaceKey || undefined;
      const result = await service()?.Connect(endpoint, nextKey);
      const key = result.connection.surfaceKey;
      localStorage.setItem("fairy.endpoint", endpoint); localStorage.setItem("fairy.surfaceKey", key); setSurfaceKey(key);
      setConnection(result); dispatch({ type: "restore", messages: result.messages.map((m: any) => ({ id: m.id, role: m.role, text: m.content, sequence: m.sequence })) }); dispatch({ type: "connected" });
    } catch (error: any) { dispatch({ type: "error", message: error?.message || "连接失败" }); }
  }
  async function saveAndConnect() {
    try { await service()?.SaveConnection(endpoint, token, surfaceKey); setToken(""); await connect(); setShowSettings(false); }
    catch (error: any) { dispatch({ type: "error", message: error?.message || "保存连接失败" }); }
  }
  async function send(form: HTMLFormElement) { const data = new FormData(form); const text = String(data.get("input") || "").trim(); if (!text || state.status === "sending") return; dispatch({ type: "send", id: `turn-${Date.now()}`, text }); try { await service()?.Send(text, false); } catch (error: any) { dispatch({ type: "failed", message: error?.message || "发送失败" }); } }
  function cancel() { service()?.Cancel().catch?.(() => undefined); dispatch({ type: "interrupted" }); }

  return <div className="app-shell">
    <header className="topbar"><div className="brand"><span className="brand-mark">✦</span><div><strong>FAIRY</strong><small>桌面伴侣</small></div></div><div className="connection"><span className={`status-dot ${connection ? "online" : ""}`} />{statusText}</div><button className="icon-button" aria-label="打开设置" title="设置" onClick={() => setShowSettings(true)}>{iconLabel("⚙")}</button></header>
    <main className="workspace"><section className="character-panel"><div className="portrait"><div className="portrait-ring" />{visualURL ? <img src={visualURL} alt={connection?.character?.name || "当前角色"} /> : <span aria-hidden="true">✦</span>}</div><div className="character-copy"><span className="eyebrow">ACTIVE CHARACTER</span><h1>{connection?.character?.name || "FAIRY"}</h1><p>{connection ? "在这里，慢慢说。" : "连接 Core 后开始聊天"}</p></div></section>
      <section className="chat-panel"><div className="chat-heading"><div><span className="eyebrow">CONVERSATION</span><h2>{connection ? "今天的对话" : "准备连接"}</h2></div><button className="icon-button quiet" aria-label="刷新会话" title="刷新" onClick={connect}>{iconLabel("↻")}</button></div><div className="transcript" aria-live="polite">{state.messages.length === 0 ? <div className="empty"><ChatBubbleIcon /><strong>{connection ? "还没有消息" : "先连接你的 FAIRY Core"}</strong><span>{connection ? "发送第一句话，让对话开始。" : "打开设置，填写 Core 地址和 Keychain token。"}</span></div> : state.messages.map((message) => <div className={`message ${message.role} ${message.pending ? "pending" : ""}`} key={message.id}>{message.role === "assistant" ? <span className="avatar">✦</span> : null}<div className="bubble">{message.text || (message.pending ? "…" : "")}</div></div>)}</div><form className="composer" onSubmit={(event) => { event.preventDefault(); void send(event.currentTarget); }}><textarea name="input" aria-label="输入消息" placeholder={connection ? "写下你想说的话…" : "连接 Core 后可发送消息"} disabled={!connection || state.status === "sending"} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} /><button type={state.status === "sending" ? "button" : "submit"} className={`send-button ${state.status === "sending" ? "cancel" : ""}`} aria-label={state.status === "sending" ? "取消发送" : "发送消息"} title={state.status === "sending" ? "取消" : "发送"} onClick={state.status === "sending" ? cancel : undefined}>{state.status === "sending" ? <Cross2Icon /> : <PaperPlaneIcon />}</button></form></section></main>
    {state.error ? <div className="error-banner"><ActivityLogIcon /><span>{state.error}</span><button onClick={() => dispatch({ type: "connected" })} aria-label="关闭错误">×</button></div> : null}
    {showSettings ? <div className="modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) setShowSettings(false); }}><section className="settings" role="dialog" aria-modal="true"><div className="settings-title"><div><span className="eyebrow">CONNECTION</span><h2>连接设置</h2></div><button className="icon-button quiet" onClick={() => setShowSettings(false)} aria-label="关闭设置">{iconLabel("×")}</button></div><label>Core endpoint<input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} /></label><label>Bearer token <small>只写入 macOS Keychain</small><input type="password" value={token} onChange={(event) => setToken(event.target.value)} placeholder="不会显示已保存 token" /></label><p className="settings-note">安装密钥由 Go 服务生成并保存在本机设置中，前端不会读取 token 明文。</p><button className="primary-button" onClick={() => void saveAndConnect()}>{iconLabel("↗")}保存并连接</button></section></div> : null}
    {!canUse ? <div className="dev-note">Wails runtime 未连接，当前为 UI 预览</div> : null}
  </div>;
}
