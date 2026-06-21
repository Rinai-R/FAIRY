import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { deriveStagePlaybackState, NEXT_ACTION } from "./stagePlayback";
import { buildBacklogItems, workflowLineAudioURL, workflowLineSpeechText } from "./stageBacklog";
import { characterVisualStyle, resolveCharacterPortrait } from "./characterVisualLayout";
import { choiceDisplayHint, choiceDisplayLabel } from "./stageChoices";
import { chapterSnapshotPosition } from "../historySnapshots";

export function GalgameStageView({
  activeCharacter, activeCharacterID, busy, input, isTyping, lastCG,
  lessonWorkflow, messages, mood, playAudio, providerLine, runtimeState, scene,
  advanceLessonWorkflow, playWorkflowNodeVoice, returnToRecords, sendChoice, sendTurn, setInput, speaking, submit,
  onOpenHome
}) {
  const [historyOpen, setHistoryOpen] = useState(false);
  const [closing, setClosing] = useState(false);
  const [lineIndex, setLineIndex] = useState(0);
  const [pendingChoiceID, setPendingChoiceID] = useState("");
  const backlogListRef = useRef(null);
  const closeTimerRef = useRef(null);
  const dialogueRef = useRef(null);

  const workflowNodes = Array.isArray(lessonWorkflow?.nodes) ? lessonWorkflow.nodes : [];
  const currentNode = workflowNodes.find((node) => node.id === lessonWorkflow?.current_node_id) || workflowNodes[0];
  const workflowHistory = Array.isArray(lessonWorkflow?.history) ? lessonWorkflow.history : [];
  const isFreeDiscussionNode = currentNode?.free_discussion || currentNode?.kind === "free_discussion";
  const stageChoices = currentNode?.choices?.length ? currentNode.choices : [];
  const speaker = activeCharacter?.display_name || activeCharacterID || "角色";
  const speakerAliases = useMemo(
    () => buildStageSpeakerAliases(activeCharacter, activeCharacterID, speaker),
    [activeCharacter, activeCharacterID, speaker],
  );
  const nodeSpeaker = resolveStageSpeaker(currentNode?.speaker, speaker, speakerAliases);

  const latestAssistant = useMemo(() => findLatest(messages, "assistant"), [messages]);
  const latestUser = useMemo(() => findLatest(messages, "user"), [messages]);
  const latestNodeAssistant = useMemo(() => findLatestForNode(messages, currentNode?.id), [messages, currentNode?.id]);

  const visibleAssistant = latestNodeAssistant || (isFreeDiscussionNode ? latestAssistant : null);
  const activeExpression = useMemo(() => {
    const seg = visibleAssistant?.segments?.find((s) => s?.expression) || visibleAssistant?.segments?.[0];
    return seg?.expression || currentNode?.expression || mood || "soft_smile";
  }, [visibleAssistant, currentNode, mood]);

  const portrait = useMemo(
    () => resolveCharacterPortrait(activeCharacter, activeExpression, mood),
    [activeCharacter, activeExpression, mood],
  );

  const backgroundFromKey = currentNode?.background_key ? activeCharacter?.assets?.backgrounds?.[currentNode.background_key] : "";
  const backgroundURL = currentNode?.background_url || backgroundFromKey
    || activeCharacter?.assets?.background_url
    || scene?.variables?.background_url || lastCG?.url || "";

  const nodeLines = currentNode?.lines?.length
    ? currentNode.lines.map((line) => normalizeStageLine(line, nodeSpeaker, speakerAliases))
    : (currentNode?.line ? [normalizeStageLine({ speaker: currentNode.speaker || nodeSpeaker, text: currentNode.line, speech_text: currentNode.speech_text, audioURL: "" }, nodeSpeaker, speakerAliases)] : []);

  const dialogueLines = visibleAssistant?.segments?.length
    ? visibleAssistant.segments.map((s, index) => normalizeStageLine({
      speaker,
      text: s.text,
      speech_text: s.speech_text || "",
      expression: s.expression,
      audioURL: nodeLines[index]?.audioURL || "",
    }, nodeSpeaker, speakerAliases))
    : nodeLines;
  const safeLineIndex = Math.min(lineIndex, Math.max(dialogueLines.length - 1, 0));
  const currentDialogueLine = dialogueLines[safeLineIndex] || null;
  const currentSpeaker = currentDialogueLine?.speaker || nodeSpeaker;
  const currentDialogueText = currentDialogueLine?.text || "";
  const currentLineAudioURL = currentDialogueLine?.audioURL || "";
  const typewriterKey = `${currentNode?.id || "scene"}:${safeLineIndex}:${currentDialogueText}`;
  const [typedText, setTypedText] = useState(currentDialogueText);
  const [typewriterDone, setTypewriterDone] = useState(true);

  const currentChapterPosition = chapterSnapshotPosition(workflowNodes, currentNode);
  const chapterKicker = isFreeDiscussionNode
    ? "FREE TALK"
    : currentChapterPosition ? `CHAPTER ${String(currentChapterPosition.number).padStart(2, "0")}` : "SCENE";
  const typingActive = !!((visibleAssistant?.meta !== "script" && visibleAssistant?.typing) || isTyping || !typewriterDone);
  const stageWaiting = Boolean(runtimeState?.stageWaiting);
  const isLastDialogueLine = dialogueLines.length > 0 && safeLineIndex >= dialogueLines.length - 1;
  const shouldShowStageChoices = stageChoices.length > 0
    && !isFreeDiscussionNode
    && isLastDialogueLine
    && typewriterDone
    && !stageWaiting
    && !pendingChoiceID;
  const playbackState = useMemo(() => deriveStagePlaybackState({
    busy,
    hasChoices: stageChoices.length > 0 && !isFreeDiscussionNode,
    hasNextNode: Boolean(currentNode?.next_node_id),
    lineCount: dialogueLines.length,
    lineIndex: safeLineIndex,
    stageWaiting,
    typewriterDone,
  }), [busy, currentNode?.next_node_id, dialogueLines.length, isFreeDiscussionNode, safeLineIndex, stageChoices.length, stageWaiting, typewriterDone]);
  const visibleDialogueLines = useMemo(() => {
    if (!currentDialogueLine) return [];
    return [{ ...currentDialogueLine, text: typedText }];
  }, [currentDialogueLine, typedText]);

  useEffect(() => {
    setLineIndex(0);
    setPendingChoiceID("");
  }, [currentNode?.id]);

  useEffect(() => {
    if (lineIndex <= safeLineIndex) return;
    setLineIndex(safeLineIndex);
  }, [lineIndex, safeLineIndex]);

  useEffect(() => {
    const chars = Array.from(currentDialogueText);
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
        setTypedText(currentDialogueText);
        setTypewriterDone(true);
        window.clearInterval(timer);
        return;
      }
      setTypedText(chars.slice(0, index).join(""));
    }, 28);
    return () => window.clearInterval(timer);
  }, [typewriterKey, currentDialogueText]);

  const playLineAudio = useCallback((index) => {
    const url = dialogueLines[index]?.audioURL || "";
    if (url) playAudio(url);
  }, [dialogueLines, playAudio]);

  const goPreviousLine = useCallback(() => {
    if (safeLineIndex <= 0 || busy) return;
    const nextIndex = safeLineIndex - 1;
    setLineIndex(nextIndex);
    playLineAudio(nextIndex);
  }, [busy, playLineAudio, safeLineIndex]);

  const goNextLineOrNode = useCallback(() => {
    switch (playbackState.nextAction) {
      case NEXT_ACTION.COMPLETE_TYPEWRITER:
        setTypedText(currentDialogueText);
        setTypewriterDone(true);
        return;
      case NEXT_ACTION.NEXT_LINE: {
        const nextIndex = safeLineIndex + 1;
        setLineIndex(nextIndex);
        playLineAudio(nextIndex);
        return;
      }
      case NEXT_ACTION.ADVANCE_NODE:
        if (currentNode?.next_node_id) advanceLessonWorkflow(currentNode.next_node_id);
        return;
      case NEXT_ACTION.WAIT_NEXT_NODE:
      case NEXT_ACTION.NONE:
      default:
        return;
    }
  }, [advanceLessonWorkflow, currentDialogueText, currentNode?.next_node_id, playLineAudio, playbackState.nextAction, safeLineIndex]);

  const handleChoice = useCallback((choice, index = 0) => {
    if (!shouldShowStageChoices || busy || pendingChoiceID) return;
    setPendingChoiceID(choice?.id || choiceDisplayLabel(choice, index));
    Promise.resolve(sendChoice(choice, index)).then((advanced) => {
      if (!advanced) setPendingChoiceID("");
    }).catch(() => {
      setPendingChoiceID("");
    });
  }, [busy, pendingChoiceID, sendChoice, shouldShowStageChoices]);

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
        goNextLineOrNode();
        return;
      }
      if (e.key === "b" || e.key === "B") {
        e.preventDefault();
        setHistoryOpen(true);
        return;
      }
      // Number keys for choices
      const num = parseInt(e.key, 10);
      if (num >= 1 && num <= 9 && shouldShowStageChoices && stageChoices.length >= num) {
        e.preventDefault();
        handleChoice(stageChoices[num - 1], num - 1);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [historyOpen, busy, isFreeDiscussionNode, currentNode, stageChoices, goNextLineOrNode, handleChoice, closeBacklog, shouldShowStageChoices]);

  return (
    <div className="vn-stage-frame">
      {/* Background */}
      <div className={`vn-stage-scene ${backgroundURL ? "has-bg" : ""}`} style={stageBG(backgroundURL)} />

      {/* Character sprite */}
      <div className="vn-character-layer">
        {portrait.url ? (
          <img
            className="vn-character-sprite"
            key={portrait.url}
            src={portrait.url}
            alt={speaker}
            style={characterVisualStyle(portrait.layout, "stage")}
          />
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
        <small>{chapterKicker}</small>
        <b>{currentNode?.title || scene?.title || "剧情演出"}</b>
      </div>

      {/* Choices */}
      {shouldShowStageChoices && (
        <div className="vn-choice-layer" key={`choices-${currentNode?.id}`}>
          {stageChoices.map((choice, idx) => (
            <button key={choice.id || choice.label || idx} onClick={() => handleChoice(choice, idx)} disabled={busy || Boolean(pendingChoiceID)}>
              <kbd>{String.fromCharCode(65 + idx)}</kbd>
              <span>
                <strong>{choiceDisplayLabel(choice, idx)}</strong>
                {choiceDisplayHint(choice) ? <em>{choiceDisplayHint(choice)}</em> : null}
              </span>
            </button>
          ))}
        </div>
      )}

      {/* Dialogue box */}
      <div className="vn-dialogue-box">
        <div className="vn-dialogue-speaker">
          <span>{currentSpeaker}</span>
          {currentNode?.summary && !isFreeDiscussionNode ? (
            <small>{currentNode.summary}</small>
          ) : null}
        </div>

        <div className="vn-dialogue-text" ref={dialogueRef}>
          {stageWaiting ? <p className="vn-stage-waiting">{runtimeState.audio || "下一幕准备中..."}</p> : null}
          {visibleDialogueLines.length ? visibleDialogueLines.map((line, i) => (
            <p key={`${currentNode?.id}-ln-${safeLineIndex}-${i}`} className={i === visibleDialogueLines.length - 1 && typingActive ? "is-typing" : ""}>
              {line.speaker !== currentSpeaker ? <b>{line.speaker}：</b> : null}
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
              <button className="vn-ctrl-btn" type="button" onClick={goPreviousLine} disabled={safeLineIndex <= 0 || busy}>上一句</button>
              <button className="vn-ctrl-btn" onClick={() => playAudio(currentLineAudioURL)} disabled={!currentLineAudioURL || speaking}>
                {speaking ? "播放中" : "重播"}
              </button>
              {playbackState.shouldShowAdvance && (
                <button className="vn-ctrl-btn vn-ctrl-advance" onClick={goNextLineOrNode} disabled={playbackState.advanceDisabled}>
                  {playbackState.advanceLabel}
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
                {buildBacklogItems(workflowHistory, workflowNodes, currentNode?.id, safeLineIndex, speaker, speakerAliases).map((item, i) => (
                  <article key={`bl-${i}`} className={`vn-backlog-item ${item.active ? "is-active" : ""} ${item.kind === "user" ? "is-player" : ""}`}>
                    <span className="vn-backlog-number">{String(i + 1).padStart(2, "0")}</span>
                    <div>
                      <strong>{item.speaker}</strong>
                      <p>{item.text}</p>
                      {item.kind === "script" && item.nodeID ? (
                        <div className="vn-backlog-actions">
                          <button type="button" onClick={() => { advanceLessonWorkflow(item.nodeID, "", true); closeBacklog(); }}>跳转</button>
                          <button type="button" onClick={() => item.audioURL ? playAudio(item.audioURL) : playWorkflowNodeVoice?.(item.node)}>复读</button>
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

function normalizeStageLine(line, fallbackSpeaker, speakerAliases) {
  const rawSpeaker = line?.speaker || fallbackSpeaker;
  const displaySpeaker = resolveStageSpeaker(rawSpeaker, fallbackSpeaker, speakerAliases);
  return {
    ...line,
    speaker: displaySpeaker,
    text: stripStageSpeakerPrefix(line?.text, [rawSpeaker, displaySpeaker, ...speakerAliases]),
    speechText: workflowLineSpeechText(line),
    audioURL: workflowLineAudioURL(line),
  };
}

function buildStageSpeakerAliases(activeCharacter, activeCharacterID, displaySpeaker) {
  return uniqueStageNames([
    displaySpeaker,
    activeCharacter?.display_name,
    activeCharacter?.id,
    activeCharacterID,
  ]);
}

function resolveStageSpeaker(value, fallbackSpeaker, speakerAliases) {
  const speaker = cleanStageSpeaker(value);
  if (!speaker) return fallbackSpeaker;
  return speakerAliases.includes(speaker) ? fallbackSpeaker : speaker;
}

function stripStageSpeakerPrefix(value, names) {
  let text = String(value || "").replace(/\s+/g, " ").trim();
  if (!text) return "";
  for (const name of uniqueStageNames(names).sort((a, b) => b.length - a.length)) {
    const escaped = escapeStageRegExp(name);
    text = text
      .replace(new RegExp(`^[【\\[]\\s*${escaped}\\s*[】\\]]\\s*`), "")
      .replace(new RegExp(`^${escaped}\\s*[：:]\\s*`), "")
      .trim();
  }
  return text;
}

function cleanStageSpeaker(value) {
  return String(value || "").replace(/[【】[\]：:]/g, "").trim();
}

function uniqueStageNames(values) {
  return [...new Set((Array.isArray(values) ? values : []).map(cleanStageSpeaker).filter(Boolean))];
}

function escapeStageRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function stageBG(url) {
  const fallback = "linear-gradient(172deg, #dfecfb 0%, #e9f2fd 44%, #f4f9ff 100%)";
  if (!url) return { background: fallback };
  return { background: `linear-gradient(180deg, rgba(255,255,255,0.14), rgba(244,250,255,0.55) 78%, rgba(244,250,255,0.86)), url("${url}") center/cover, ${fallback}` };
}
