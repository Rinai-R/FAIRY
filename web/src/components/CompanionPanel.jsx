import { Transcript } from "./Transcript.jsx";

function IconArrow({ direction = "up" }) {
  return (
    <svg className={`icon icon--${direction}`} viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="m6 14 6-6 6 6" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function IconSettings() {
  return (
    <svg className="icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <circle cx="12" cy="12" r="3" stroke="currentColor" strokeWidth="1.6" />
      <path d="M19 12a7 7 0 0 0-.1-1.2l2-1.5-2-3.4-2.4 1a8 8 0 0 0-2-1.2L14.2 3h-4.1l-.3 2.7a8 8 0 0 0-2 1.2l-2.4-1-2 3.4 2 1.5A7 7 0 0 0 5.2 12c0 .4 0 .8.1 1.2l-2 1.5 2 3.4 2.4-1a8 8 0 0 0 2 1.2l.3 2.7h4.1l.3-2.7a8 8 0 0 0 2-1.2l2.4 1 2-3.4-2-1.5c.1-.4.2-.8.2-1.2Z" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

export function CompanionPanel({
  open,
  onToggle,
  onOpenSettings,
  character,
  preferredName,
  companion,
  onDraftChange,
  onSubmit,
  onCancel,
}) {
  const characterName = character?.name ?? "FAIRY";
  const ready = companion.conversationId !== null && character !== null;
  const active = companion.activeTurnId !== null || companion.submitting;

  function handleSubmit(event) {
    event.preventDefault();
    onSubmit();
  }

  function handleKeyDown(event) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      if (!active && companion.draft.trim().length > 0) onSubmit();
    }
  }

  if (!open) {
    return (
      <button className="letter-tab" type="button" onClick={onToggle} aria-expanded="false">
        <span className="letter-tab__mark" aria-hidden="true">✦</span>
        <span>
          <strong>和 {characterName} 说说话</strong>
          <small>{preferredName ? `她会记得称呼你为 ${preferredName}` : "此刻就可以开始"}</small>
        </span>
        <IconArrow />
      </button>
    );
  }

  return (
    <section className="companion-panel reveal" aria-label="陪伴对话">
      <header className="companion-panel__header">
        <div>
          <span className="eyebrow">NOW WITH YOU</span>
          <h2>{characterName}</h2>
        </div>
        <div className="panel-actions">
          <button className="icon-button" type="button" onClick={onOpenSettings} aria-label="打开角色与连接设置">
            <IconSettings />
          </button>
          <button className="icon-button" type="button" onClick={onToggle} aria-label="收起对话">
            <IconArrow direction="down" />
          </button>
        </div>
      </header>

      {!ready ? (
        <div className="panel-onboarding" role="status">
          <span aria-hidden="true">✦</span>
          <h3>还差一点，她就能听见你了。</h3>
          <p>先选择角色，并配置你愿意使用的模型连接。</p>
          <button className="btn btn-primary" type="button" onClick={onOpenSettings}>去设置</button>
        </div>
      ) : (
        <>
          <Transcript
            characterName={characterName}
            transcript={companion.transcript}
            responseDraft={companion.responseDraft}
            sessionState={companion.sessionState}
          />

          {companion.error ? (
            <div className="inline-error" role="alert">
              <strong>{companion.error.code}</strong>
              <span>{companion.error.message}</span>
            </div>
          ) : null}

          <form className="chat-input-area" onSubmit={handleSubmit}>
            <div className="chat-input-wrap">
              <textarea
                className="chat-textarea"
                value={companion.draft}
                onChange={(event) => onDraftChange(event.target.value)}
                onKeyDown={handleKeyDown}
                rows="1"
                placeholder={`想对 ${characterName} 说什么？`}
                aria-label="消息输入框"
                disabled={active}
              />
              {active && companion.activeTurnId ? (
                <button className="chat-cancel-btn" type="button" onClick={onCancel}>停止</button>
              ) : (
                <button
                  className="chat-send-btn"
                  type="submit"
                  disabled={active || companion.draft.trim().length === 0}
                  aria-label="发送消息"
                >
                  <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
                    <path d="M5 12h14m-6-6 6 6-6 6" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
                  </svg>
                </button>
              )}
            </div>
            <p className="chat-input-hint">Enter 发送 · Shift + Enter 换行</p>
          </form>
        </>
      )}
    </section>
  );
}
