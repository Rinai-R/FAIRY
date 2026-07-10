import { useEffect, useRef } from "react";

export function Transcript({
  characterName,
  transcript,
  responseDraft,
  sessionState,
}) {
  const endRef = useRef(null);

  useEffect(() => {
    endRef.current?.scrollIntoView({ block: "nearest" });
  }, [transcript, responseDraft, sessionState]);

  return (
    <div
      className="chat-messages"
      role="log"
      aria-live="polite"
      aria-label="对话消息"
    >
      {transcript.length === 0 && responseDraft.length === 0 ? (
        <div className="transcript-empty">
          <span aria-hidden="true">✦</span>
          <p>不用想好开场白。</p>
          <small>说一句此刻最自然的话就好。</small>
        </div>
      ) : null}

      {transcript.map((message, index) => (
        <article
          className={`chat-msg chat-msg--${message.role === "assistant" ? "ai" : "user"}`}
          key={`${message.role}-${index}-${message.text}`}
        >
          {message.role === "assistant" ? (
            <div className="chat-msg__meta">
              <span className="chat-msg__ai-avatar" aria-hidden="true">✦</span>
              <span className="chat-msg__sender">{characterName}</span>
            </div>
          ) : null}
          <div className="chat-msg__content">
            <p>{message.text}</p>
          </div>
        </article>
      ))}

      {responseDraft.length > 0 ? (
        <article
          className="chat-msg chat-msg--ai chat-msg--streaming"
          aria-label={`${characterName} 的未完成回复`}
        >
          <div className="chat-msg__meta">
            <span className="chat-msg__ai-avatar" aria-hidden="true">✦</span>
            <span className="chat-msg__sender">{characterName}</span>
            <span className="chat-msg__phase">
              {sessionState === "failed" || sessionState === "interrupted"
                ? "回复未完成"
                : "正在回复"}
            </span>
          </div>
          <div className="chat-msg__content">
            <p>{responseDraft}</p>
          </div>
        </article>
      ) : null}

      {responseDraft.length === 0 && ["interpreting", "planning", "responding"].includes(sessionState) ? (
        <div className="chat-msg chat-msg--ai chat-msg--streaming" aria-busy="true">
          <div className="chat-msg__meta">
            <span className="chat-msg__ai-avatar" aria-hidden="true">✦</span>
            <span className="chat-msg__sender">{characterName}</span>
          </div>
          <span className="chat-typing-indicator" aria-label={`${characterName} 正在想`}>
            <span /><span /><span />
          </span>
        </div>
      ) : null}
      <div ref={endRef} />
    </div>
  );
}
