import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";

export function GalgameStageView({
  activeCharacter, activeCharacterID, busy, input, isTyping, lastCG,
  lessonWorkflow, messages, mood, playAudio, providerLine, runtimeState, scene,
  advanceLessonWorkflow, playWorkflowNodeVoice, returnToRecords, sendChoice, sendTurn, setInput, speaking, submit,
  onOpenHome
}) {
  const [historyOpen, setHistoryOpen] = useState(false);
  const [closing, setClosing] = useState(false);
  const backlogListRef = useRef(null);
  const closeTimerRef = useRef(null);
  const dialogueRef = useRef(null);

  const workflowNodes = Array.isArray(lessonWorkflow?.nodes) ? lessonWorkflow.nodes : [];
  const currentNode = workflowNodes.find((node) => node.id === lessonWorkflow?.current_node_id) || workflowNodes[0];
  const workflowHistory = Array.isArray(lessonWorkflow?.history) ? lessonWorkflow.history : [];
  const isFreeDiscussionNode = currentNode?.free_discussion || currentNode?.kind === "free_discussion";
  const stageChoices = currentNode?.choices?.length ? currentNode.choices : [];
  const speaker = activeCharacter?.display_name || activeCharacterID || "角色";

  const latestAssistant = useMemo(() => findLatest(messages, "assistant"), [messages]);
  const latestUser = useMemo(() => findLatest(messages, "user"), [messages]);
  const latestNodeAssistant = useMemo(() => findLatestForNode(messages, currentNode?.id), [messages, currentNode?.id]);

  const visibleAssistant = latestNodeAssistant || (isFreeDiscussionNode ? latestAssistant : null);
  const activeExpression = useMemo(() => {
    const seg = visibleAssistant?.segments?.find((s) => s?.expression) || visibleAssistant?.segments?.[0];
    return seg?.expression || currentNode?.expression || mood || "soft_smile";
  }, [visibleAssistant, currentNode, mood]);

  const portrait = activeCharacter?.assets?.moods?.[activeExpression]?.portrait_url
    || activeCharacter?.assets?.moods?.[mood]?.portrait_url
    || activeCharacter?.assets?.portrait_url
    || activeCharacter?.avatar_url || "";

  const backgroundFromKey = currentNode?.background_key ? activeCharacter?.assets?.backgrounds?.[currentNode.background_key] : "";
  const backgroundURL = currentNode?.background_url || backgroundFromKey
    || activeCharacter?.assets?.background_url
    || scene?.variables?.background_url || lastCG?.url || "";

  const nodeLines = currentNode?.lines?.length
    ? currentNode.lines.map((line) => ({ ...line, speechText: workflowLineSpeechText(line) }))
    : (currentNode?.line ? [{ speaker: currentNode.speaker || speaker, text: currentNode.line, speechText: currentNode.speech_text || "" }] : []);

  const dialogueLines = visibleAssistant?.segments?.length
    ? visibleAssistant.segments.map((s) => ({ speaker, text: s.text, speechText: s.speech_text || "", expression: s.expression }))
    : nodeLines;
  const lastDialogueText = dialogueLines[dialogueLines.length - 1]?.text || "";
  const typewriterKey = `${currentNode?.id || "scene"}:${lastDialogueText}`;
  const [typedText, setTypedText] = useState(lastDialogueText);
  const [typewriterDone, setTypewriterDone] = useState(true);

  const currentNodeIdx = workflowNodes.findIndex((n) => n.id === currentNode?.id);
  const typingActive = !!(visibleAssistant?.typing || isTyping || !typewriterDone);
  const stageWaiting = Boolean(runtimeState?.stageWaiting);
  const visibleDialogueLines = useMemo(() => {
    if (!dialogueLines.length) return dialogueLines;
    return dialogueLines.map((line, index) => (
      index === dialogueLines.length - 1 ? { ...line, text: typedText } : line
    ));
  }, [dialogueLines, typedText]);

  useEffect(() => {
    const chars = Array.from(lastDialogueText);
    if (!chars.length) {
      setTypedText("");
      setTypewriterDone(true);
      return undefined;
    }
    setTypedText("");
    setTypewriterDone(false);
    let index = 0;
    const timer = window.setInterval(() => {
      index += chars[index]?.match(/[，。！？；、,.!?;:]/) ? 1 : 2;
      if (index >= chars.length) {
        setTypedText(lastDialogueText);
        setTypewriterDone(true);
        window.clearInterval(timer);
        return;
      }
      setTypedText(chars.slice(0, index).join(""));
    }, 28);
    return () => window.clearInterval(timer);
  }, [typewriterKey, lastDialogueText]);

  // Auto-scroll dialogue
  useEffect(() => {
    if (dialogueRef.current) {
      dialogueRef.current.scrollTop = dialogueRef.current.scrollHeight;
    }
  }, [visibleDialogueLines, visibleAssistant?.text]);

  // Backlog auto-scroll
  useEffect(() => {
    if (!historyOpen || closing) return;
    const timer = window.setTimeout(() => {
      const el = backlogListRef.current?.querySelector(".vn-backlog-item.is-active");
      el?.scrollIntoView({ block: "center", behavior: "smooth" });
    }, 220);
    return () => window.clearTimeout(timer);
  }, [historyOpen, closing]);

  const closeBacklog = useCallback(() => {
    if (!historyOpen || closing) return;
    setClosing(true);
    closeTimerRef.current = window.setTimeout(() => { setHistoryOpen(false); setClosing(false); }, 220);
  }, [historyOpen, closing]);

  // Keyboard
  useEffect(() => {
    const handleKeyDown = (e) => {
      if (e.target.tagName === "TEXTAREA" || e.target.tagName === "INPUT") return;

      if (historyOpen) {
        if (e.key === "Escape") { e.preventDefault(); closeBacklog(); }
        return;
      }

      if (busy) return;

      if ((e.key === " " || e.key === "Enter") && !isFreeDiscussionNode) {
        e.preventDefault();
        if (currentNode?.next_node_id) advanceLessonWorkflow(currentNode.next_node_id);
        return;
      }
      if (e.key === "b" || e.key === "B") {
        e.preventDefault();
        setHistoryOpen(true);
        return;
      }
      // Number keys for choices
      const num = parseInt(e.key, 10);
      if (num >= 1 && num <= 9 && stageChoices.length >= num) {
        e.preventDefault();
        sendChoice(stageChoices[num - 1]);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [historyOpen, busy, isFreeDiscussionNode, currentNode, stageChoices, advanceLessonWorkflow, sendChoice, closeBacklog]);

  return (
    <div className="vn-stage-frame">
      {/* Background */}
      <div className={`vn-stage-scene ${backgroundURL ? "has-bg" : ""}`} style={stageBG(backgroundURL)} />

      {/* Character sprite */}
      <div className="vn-character-layer">
        {portrait ? (
          <img className="vn-character-sprite" key={portrait} src={portrait} alt={speaker} />
        ) : (
          <div className="vn-character-fallback">
            <strong>{speaker.slice(0, 2)}</strong>
          </div>
        )}
      </div>

      {/* Back to preview */}
      <button className="vn-back-btn" onClick={onOpenHome} aria-label="返回剧情预览" title="返回剧情预览">
        ←
      </button>

      <div className="vn-chapter-label" aria-label="当前章节">
        <small>{currentNodeIdx >= 0 ? `CHAPTER ${String(currentNodeIdx + 1).padStart(2, "0")}` : "CHAPTER"}</small>
        <b>{currentNode?.title || scene?.title || "剧情演出"}</b>
      </div>

      {/* Choices */}
      {stageChoices.length > 0 && !isFreeDiscussionNode && (
        <div className="vn-choice-layer" key={`choices-${currentNode?.id}`}>
          {stageChoices.map((choice, idx) => (
            <button key={choice.id || choice.label} onClick={() => sendChoice(choice)} disabled={busy}>
              <kbd>{String.fromCharCode(65 + idx)}</kbd>
              <span>
                <strong>{choice.label}</strong>
                {choice.hint ? <em>{choice.hint}</em> : null}
              </span>
            </button>
          ))}
        </div>
      )}

      {/* Dialogue box */}
      <div className="vn-dialogue-box">
        <div className="vn-dialogue-speaker">
          <span>{currentNode?.speaker || speaker}</span>
          {currentNode?.summary && !isFreeDiscussionNode ? (
            <small>{currentNode.summary}</small>
          ) : null}
        </div>

        <div className="vn-dialogue-text" ref={dialogueRef}>
          {stageWaiting ? <p className="vn-stage-waiting">{runtimeState.audio || "下一幕准备中..."}</p> : null}
          {visibleDialogueLines.length ? visibleDialogueLines.map((line, i) => (
            <p key={`${currentNode?.id}-ln-${i}`} className={i === visibleDialogueLines.length - 1 && typingActive ? "is-typing" : ""}>
              {line.speaker !== speaker ? <b>{line.speaker}：</b> : null}
              {line.text}
              {i === visibleDialogueLines.length - 1 && typingActive ? <i className="vn-cursor" /> : null}
            </p>
          )) : (
            <p className="vn-dialogue-empty">
              {currentNode?.summary || "等待 Agent 生成对话..."}
              {typingActive ? <i className="vn-cursor" /> : null}
            </p>
          )}
        </div>

        {/* Controls */}
        <div className="vn-dialogue-actions">
          {isFreeDiscussionNode ? (
            <form className="vn-composer" onSubmit={submit}>
              <input
                value={input}
                onChange={(e) => setInput(e.target.value)}
                placeholder="针对这一幕的内容提问，回车发送..."
                autoFocus
              />
              <button type="submit" disabled={busy || !input.trim()}>发送</button>
              {currentNode?.next_node_id && (
                <button type="button" className="vn-skip-btn" onClick={() => advanceLessonWorkflow(currentNode.next_node_id)} disabled={busy || stageWaiting}>跳过 ▸</button>
              )}
            </form>
          ) : (
            <>
              <button className="vn-ctrl-btn" type="button" disabled>上一句</button>
              <button className="vn-ctrl-btn" onClick={() => playAudio()} disabled={!runtimeState.audio || runtimeState.audio === "-" || speaking}>
                {speaking ? "播放中" : "重播"}
              </button>
              {currentNode?.next_node_id && (
                <button className="vn-ctrl-btn vn-ctrl-advance" onClick={() => advanceLessonWorkflow(currentNode.next_node_id)} disabled={busy || stageWaiting}>
                  {stageWaiting ? "准备中" : "下一句"}
                </button>
              )}
              <button className="vn-ctrl-btn" type="button" disabled>自动</button>
              <button className="vn-ctrl-btn" type="button" disabled>快进</button>
              <button className="vn-ctrl-btn" type="button" onClick={() => setHistoryOpen(true)}>历史</button>
              <button className="vn-ctrl-btn" type="button" disabled>存档</button>
            </>
          )}
        </div>
      </div>

      {/* Backlog overlay */}
      {historyOpen && (
        <div className={`vn-backlog-overlay ${closing ? "is-closing" : ""}`}
          onMouseDown={(e) => { if (e.target === e.currentTarget) closeBacklog(); }}>
          <div className="vn-backlog-panel">
            <div className="vn-backlog-top">
              <div>
                <span className="eyebrow">回想记录</span>
                <h2>剧情记录</h2>
              </div>
              <button onClick={closeBacklog}>关闭</button>
            </div>
            <div className="vn-backlog-body">
              <div className="vn-backlog-list" ref={backlogListRef}>
                {buildBacklogItems(messages, workflowHistory, workflowNodes, currentNode?.id, speaker).reverse().map((item, i) => (
                  <article key={`bl-${i}`} className={`vn-backlog-item ${item.active ? "is-active" : ""} ${item.kind === "user" ? "is-player" : ""}`}>
                    <span className="vn-backlog-number">{String(i + 1).padStart(2, "0")}</span>
                    <div>
                      <strong>{item.speaker}</strong>
                      <p>{item.text}</p>
                      {item.kind === "script" && item.nodeID ? (
                        <div className="vn-backlog-actions">
                          <button type="button" onClick={() => { advanceLessonWorkflow(item.nodeID, "", true); closeBacklog(); }}>跳转</button>
                          <button type="button" onClick={() => playWorkflowNodeVoice?.(item.node)}>复读</button>
                        </div>
                      ) : null}
                    </div>
                  </article>
                ))}
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ---- helpers ----

function findLatest(messages, role) {
  for (let i = messages.length - 1; i >= 0; i--) if (messages[i]?.role === role) return messages[i];
  return null;
}

function findLatestForNode(messages, nodeID) {
  if (!nodeID) return null;
  for (let i = messages.length - 1; i >= 0; i--) if (messages[i]?.role === "assistant" && messages[i].nodeID === nodeID) return messages[i];
  return null;
}

function buildBacklogItems(messages, workflowHistory, workflowNodes, currentNodeID, assistantSpeaker) {
  const nodeByID = new Map(workflowNodes.map((n) => [n.id, n]));
  const items = workflowHistory.map((h) => {
    const node = nodeByID.get(h.node_id);
    return {
      kind: "script", active: h.node_id === currentNodeID,
      nodeID: h.node_id,
      node,
      speaker: node?.speaker || "角色",
      text: h.choice_label ? `选择：${h.choice_label}` : (workflowNodeDisplayText(node) || h.action || "进入剧情"),
    };
  });
  return [...items, ...messages.filter((m) => m?.text).map((m) => ({
    kind: m.role || "message", active: false,
    speaker: m.role === "user" ? "你" : assistantSpeaker || "角色",
    text: m.text,
  }))].slice(-40);
}

function workflowLineSpeechText(line) {
  return String(line?.speech_text || line?.speechText || line?.text || "").trim();
}

function workflowNodeDisplayText(node) {
  const lines = Array.isArray(node?.lines) ? node.lines : [];
  if (lines.length) return lines.map((line) => String(line?.text || "").trim()).filter(Boolean).join(" ");
  return String(node?.line || "").trim();
}

function stageBG(url) {
  const fallback = "linear-gradient(172deg, #dfecfb 0%, #e9f2fd 44%, #f4f9ff 100%)";
  if (!url) return { background: fallback };
  return { background: `linear-gradient(180deg, rgba(255,255,255,0.14), rgba(244,250,255,0.55) 78%, rgba(244,250,255,0.86)), url("${url}") center/cover, ${fallback}` };
}
