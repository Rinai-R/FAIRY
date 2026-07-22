import { useEffect, useMemo, useReducer, useState } from "react";
import { ChatBubbleIcon, Cross2Icon, GearIcon, PaperPlaneIcon, StopIcon } from "@radix-ui/react-icons";
import { chatReducer, initialChatState } from "./state";

declare global { interface Window { go?: { main?: { AppService?: any } }; runtime?: { EventsOn?: (name: string, cb: (value: any) => void) => (() => void) | void } } }

const service = () => window.go?.main?.AppService;

export default function App() {
  const [state, dispatch] = useReducer(chatReducer, initialChatState);
  const [endpoint, setEndpoint] = useState(localStorage.getItem("fairy.endpoint") || "http://127.0.0.1:8787");
  const [endpointKey, setEndpointKey] = useState(localStorage.getItem("fairy.endpointKey") || "");
  const [token, setToken] = useState("");
  const [showSettings, setShowSettings] = useState(false);
  const [chatOpen, setChatOpen] = useState(false);
  const [connection, setConnection] = useState<any>(null);
  const [visualURL, setVisualURL] = useState("");
  const canUse = Boolean(service());
  const recentMessages = useMemo(() => state.messages.slice(-4), [state.messages]);
  const busy = state.status === "sending";
  const statusText = state.status === "connecting" ? "连接中" : busy ? "回复中" : state.status === "error" ? "需要处理" : connection ? "在这里" : "未连接";

  useEffect(() => {
    const off = window.runtime?.EventsOn?.("turn:event", (event: any) => {
      const type = event.type || event.Type;
      const turnState = event.turnEvent?.state || event.TurnEvent?.State;
      if (type === "beat.ready" && (event.beat || event.Beat)) dispatch({ type: "beat", beat: event.beat || event.Beat });
      else if (type === "stream.closed") dispatch({ type: "stream_closed", message: event.message || "与 Core 的会话连接已断开" });
      else if (turnState === "completed") dispatch({ type: "completed" });
      else if (turnState === "interrupted") dispatch({ type: "interrupted" });
      else if (turnState === "failed" || type === "failed") dispatch({ type: "failed", message: event.failure?.message || event.message || "Core 返回失败" });
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
      const result = await service()?.Connect(endpoint, endpointKey || undefined);
      const key = result.connection.endpointKey;
      localStorage.setItem("fairy.endpoint", endpoint);
      localStorage.setItem("fairy.endpointKey", key);
      setEndpointKey(key);
      setConnection(result);
      dispatch({ type: "restore", messages: result.messages.map((message: any) => ({ id: message.id, role: message.role, text: message.content, sequence: message.sequence })) });
      dispatch({ type: "connected" });
    } catch (error: any) {
      dispatch({ type: "error", message: error?.message || "连接失败" });
    }
  }

  useEffect(() => {
    if (canUse) void connect();
  }, []);

  async function saveAndConnect() {
    try {
      await service()?.SaveConnection(endpoint, token, endpointKey);
      setToken("");
      await connect();
      setShowSettings(false);
    } catch (error: any) {
      dispatch({ type: "error", message: error?.message || "保存连接失败" });
    }
  }

  async function send(form: HTMLFormElement) {
    const text = String(new FormData(form).get("input") || "").trim();
    if (!text || busy || !connection) return;
    dispatch({ type: "send", id: `turn-${Date.now()}`, text });
    form.reset();
    try {
      await service()?.Send(text, false);
    } catch (error: any) {
      dispatch({ type: "failed", message: error?.message || "发送失败" });
    }
  }

  async function cancel() {
    try {
      await service()?.Cancel();
    } catch (error: any) {
      dispatch({ type: "failed", message: error?.message || "取消失败" });
    }
  }

  return <main className="fairy-companion">
    <button className="settings-button" type="button" aria-label="打开设置" title="设置" onClick={() => setShowSettings(true)}><GearIcon /></button>
    <section className="fairy-pet" aria-label={connection?.character?.name || "桌面角色"}>
      <div className="fairy-pet__character" data-state={state.visualState}>
        {visualURL ? <img src={visualURL} alt={connection?.character?.name || "当前角色"} /> : <span className="character-placeholder">...</span>}
      </div>
      <p className="pet-status" aria-live="polite">{statusText}</p>
      <button className="fairy-chat-trigger" type="button" onClick={() => setChatOpen(true)} disabled={!connection}>
        <ChatBubbleIcon />聊一会儿
      </button>
    </section>

    {chatOpen ? <section className="fairy-chat-card" aria-label="对话">
      <div className="chat-title"><span>{connection?.character?.name || "FAIRY"}</span><div><button className="tool-button" type="button" aria-label="打开设置" title="设置" onClick={() => setShowSettings(true)}><GearIcon /></button><button className="tool-button" type="button" aria-label="收起对话" title="收起" disabled={busy} onClick={() => setChatOpen(false)}><Cross2Icon /></button></div></div>
      <div className="transcript" role="log" aria-live="polite">
        {recentMessages.length === 0 && !busy ? <p className="empty-chat">...</p> : null}
        {recentMessages.map((message) => <article className={`message ${message.role} ${message.pending ? "pending" : ""}`} key={message.id}><p>{message.text || "..."}</p></article>)}
      </div>
      <form className="composer" onSubmit={(event) => { event.preventDefault(); void send(event.currentTarget); }}>
        <textarea name="input" rows={1} aria-label="输入消息" placeholder={connection ? "说点什么..." : ""} disabled={!connection || busy} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} />
        {busy ? <button className="send-button cancel" type="button" aria-label="停止" title="停止" onClick={() => void cancel()}><StopIcon /></button> : <button className="send-button" type="submit" aria-label="发送" title="发送" disabled={!connection}><PaperPlaneIcon /></button>}
      </form>
    </section> : null}

    {state.error ? <div className="error-banner" role="alert"><span>{state.error}</span><button type="button" aria-label="关闭错误" onClick={() => dispatch({ type: "connected" })}><Cross2Icon /></button></div> : null}

    {showSettings ? <div className="modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) setShowSettings(false); }}><section className="settings" role="dialog" aria-modal="true" aria-label="连接设置">
      <div className="settings-title"><span>连接设置</span><button className="tool-button" type="button" aria-label="关闭设置" onClick={() => setShowSettings(false)}><Cross2Icon /></button></div>
      <label>Core 地址<input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} /></label>
      <label>访问令牌<input type="password" value={token} onChange={(event) => setToken(event.target.value)} placeholder="本机未认证可留空" /></label>
      <button className="connect-button" type="button" onClick={() => void saveAndConnect()}>连接</button>
    </section></div> : null}
    {!canUse ? <span className="preview-note">预览</span> : null}
  </main>;
}
