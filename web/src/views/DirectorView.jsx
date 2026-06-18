import React from "react";
import { expressionLabel, motionLabel } from "./displayLabels";

export function DirectorView({
  activeCharacter,
  activeCharacterID,
  busy,
  documentAsset,
  documentTitle,
  lastCG,
  lessonWorkflow,
  messages,
  mood,
  runtimeState,
  scene,
  sessions,
  setActiveView
}) {
  const allNodes = Array.isArray(lessonWorkflow?.nodes) ? lessonWorkflow.nodes : [];
  const beats = allNodes.filter((n) => n.kind === "lesson" || n.kind === "opening");
  const history = Array.isArray(lessonWorkflow?.history) ? lessonWorkflow.history : [];
  const currentBeat = allNodes.find((beat) => beat.id === lessonWorkflow?.current_node_id) || allNodes[0];
  const visitedBeatIDs = new Set(history.map((item) => item.node_id));
  const latestAssistant = findLatestMessage(messages, "assistant");
  const latestBeatAssistant = findLatestBeatMessage(messages, currentBeat?.id);
  const visibleAssistant = latestBeatAssistant || latestAssistant;
  const activeSegment = visibleAssistant?.segments?.find((segment) => segment?.expression) || visibleAssistant?.segments?.[0];
  const activeExpression = normalizeMood(activeSegment?.expression || currentBeat?.expression || runtimeState.expression || mood);
  const activeExpressionLabel = expressionLabel(activeExpression, activeCharacter);
  const activeMotionLabel = motionLabel(runtimeState.motion || currentBeat?.kind);
  const portrait = activeCharacter?.assets?.moods?.[activeExpression]?.portrait_url || activeCharacter?.assets?.moods?.[mood]?.portrait_url || activeCharacter?.assets?.portrait_url || activeCharacter?.avatar_url || "";
  const backgroundFromKey = currentBeat?.background_key ? activeCharacter?.assets?.backgrounds?.[currentBeat.background_key] : "";
  const backgroundURL = currentBeat?.background_url || backgroundFromKey || activeCharacter?.assets?.background_url || scene.variables?.background_url || lastCG?.url || "";
  const choices = currentBeat?.choices?.length ? currentBeat.choices : [];
  const beatIndex = Math.max(0, beats.findIndex((beat) => beat.id === currentBeat?.id));
  const progress = progressPercent(Math.max(history.length, beatIndex + 1), beats.length);
  const hasPlayableBeat = Boolean(currentBeat && currentBeat.kind !== "draft" && (beats.length > 0 || choices.length > 0 || currentBeat.free_discussion));
  const previewStyle = galSceneBackgroundStyle(backgroundURL, "rgba(20, 19, 22, 0.02)", "rgba(20, 19, 22, 0.28)");
  const beatLines = currentBeat?.lines?.length ? currentBeat.lines : (currentBeat?.line ? [{ speaker: currentBeat.speaker, text: currentBeat.line }] : []);
  const previewLines = hasPlayableBeat ? makePreviewLines(beatLines, visibleAssistant, 2) : [];
  const materialName = documentAsset?.filename || documentTitle || "未导入材料";
  const directorTitle = hasPlayableBeat ? (scene.title || lessonWorkflow?.title || "演出预览") : "演出预览";
  const coverTitle = hasPlayableBeat
    ? (currentBeat?.title || "当前章节")
    : "还没有可播放的剧情";
  const coverLine = hasPlayableBeat
    ? previewLines.map((line) => line.text).join(" ")
    : "先回到主页生成教学剧情。生成完成后，这里会变成正式的演出入口。";
  const chapterText = beats.length ? `${beatIndex + 1}/${beats.length}` : "0/0";
  const enterLabel = hasPlayableBeat ? "进入演出" : "去生成";

  return (
    <section className="page director-page">
      <header className="director-hero">
        <div>
          <span className="eyebrow">剧情演出</span>
          <h1>{directorTitle}</h1>
          <p>这里只确认章节、角色和入口状态。正式阅读会切到全屏视觉小说演出。</p>
        </div>
        <div className="director-hero-actions">
          <button className="ghost-button" type="button" onClick={() => setActiveView("history")}>记录</button>
          <button className="primary-button" type="button" onClick={() => setActiveView(hasPlayableBeat ? "stage" : "dashboard")}>{enterLabel}</button>
        </div>
      </header>

      <div className="director-shell">
        <section className={`director-preview ${backgroundURL ? "has-background" : ""}`} style={previewStyle}>
          <div className="director-preview__top">
            <div>
              <span className="director-preview__badge">{hasPlayableBeat ? `CHAPTER ${chapterText}` : "DRAFT"}</span>
              <h2>{coverTitle}</h2>
            </div>
            <span className="director-preview__hint">{hasPlayableBeat ? "使用顶部入口进入正式演出" : "等待生成剧情"}</span>
          </div>
          <div className="director-character-layer">
            {portrait ? (
              <img src={portrait} alt={activeCharacter?.display_name || activeCharacterID || "角色"} />
            ) : (
              <div className="director-character-fallback">
                <strong>{(activeCharacter?.display_name || activeCharacterID || "角色").slice(0, 2)}</strong>
                <span>{activeExpressionLabel}</span>
              </div>
            )}
          </div>
          <div className="director-dialogue">
            <div className="director-dialogue__head">
              <strong>{currentBeat?.speaker || activeCharacter?.display_name || activeCharacterID || "角色"}</strong>
              <span>{activeExpressionLabel} · {activeMotionLabel}</span>
            </div>
            {previewLines.length ? (
              <div className="director-dialogue__lines" aria-label="当前幕摘录">
                {previewLines.map((line, index) => (
                  <p key={`${currentBeat?.id || "preview"}-${index}`}>{line.text}</p>
                ))}
              </div>
            ) : (
              <p>{coverLine}</p>
            )}
            {choices.length ? <small>{`本幕包含 ${choices.length} 个正式演出选项`}</small> : null}
          </div>
        </section>

        <aside className="director-inspector">
          <section className="director-status-card director-cast-card">
            <div className="director-card-head">
              <span className="eyebrow">演出状态</span>
              <strong>{hasPlayableBeat ? "可进入" : "待生成"}</strong>
            </div>
            <div className="director-pills">
              <span>{activeCharacter?.display_name || activeCharacterID || "角色"}</span>
              <span>{activeExpressionLabel}</span>
              <span>{backgroundURL ? "背景已绑定" : "等待背景"}</span>
            </div>
            <p>{hasPlayableBeat ? "章节已经具备台词，可以进入全屏演出。" : "还没有生成可播放章节，请先从主页生成剧情。"}</p>
          </section>
          <section className="director-status-card">
            <div className="director-card-head">
              <span className="eyebrow">沉浸页布局</span>
            </div>
            <p>进入后隐藏左侧项目导航，舞台铺满窗口，底部固定对白与选项，章节进度收成细线。</p>
          </section>
          <section className="director-status-card">
            <div className="director-card-head">
              <span className="eyebrow">材料</span>
              <strong>{documentAsset?.filename ? "文件" : "主题"}</strong>
            </div>
            <p>{materialName}</p>
            <p>{lessonWorkflow?.goal || scene.variables?.outline || "等待设置学习目标。"}</p>
          </section>
          <section className="director-status-card">
            <div className="director-card-head">
              <span className="eyebrow">章节进度</span>
              <strong>{chapterText}</strong>
            </div>
            <div className="director-progress director-progress--wide" aria-label="剧情进度">
              <span style={{ width: `${progress}%` }} />
            </div>
            <div className="director-resource-list">
              <StageMetric label="记录" value={String(sessions.length)} />
              <StageMetric label="音频" value={runtimeState.audio || "-"} />
            </div>
          </section>
        </aside>
      </div>

      <div className="director-filmstrip" aria-label="章节胶片">
        {beats.length ? beats.map((beat, index) => {
          const visited = visitedBeatIDs.has(beat.id);
          const isCurrent = beat.id === currentBeat?.id;
          const status = isCurrent ? "当前预览" : visited ? "已生成 · 可回看" : "未开始";
          return (
            <button
              key={beat.id}
              type="button"
              className={`film-card ${isCurrent ? "is-active" : ""} ${visited ? "is-visited" : ""}`}
              disabled={busy || !visited || isCurrent}
            >
              <div className="film-card__top">
                <span className="film-card__idx">{String(index + 1).padStart(2, "0")}</span>
                <i className={`film-card__dot ${visited ? "is-on" : ""}`} />
              </div>
              <strong>{beat.title || `第 ${index + 1} 幕`}</strong>
              <small>{status}</small>
            </button>
          );
        }) : (
          <div className="film-empty">还没有生成章节。先从主页导入材料并生成剧情，这里会出现章节胶片。</div>
        )}
      </div>
    </section>
  );
}

function StageMetric({ label, value }) {
  return (
    <div className="director-stage-metric">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function findLatestMessage(messages, role) {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index]?.role === role) return messages[index];
  }
  return null;
}

function findLatestBeatMessage(messages, beatID) {
  if (!beatID) return null;
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (message?.role === "assistant" && message.nodeID === beatID) return message;
  }
  return null;
}

function galSceneBackgroundStyle(url, topOverlay, bottomOverlay) {
  const fallback = "linear-gradient(180deg, #dfecfb 0%, #e9f2fd 46%, #f4f9ff 100%)";
  if (!url) {
    return { backgroundImage: fallback };
  }
  return {
    backgroundColor: "#e9f2fd",
    backgroundImage: `linear-gradient(180deg, ${topOverlay}, ${bottomOverlay}), url("${url}"), ${fallback}`
  };
}

function progressPercent(visited, total) {
  if (!total) return 0;
  return Math.min(100, Math.max(0, Math.round((visited / total) * 100)));
}

function normalizeMood(value) {
  const raw = String(value || "").trim().toLowerCase();
  if (!raw) return "soft_smile";
  if (raw.includes("happy") || raw.includes("joy")) return "happy";
  if (raw.includes("curious") || raw.includes("question")) return "curious";
  if (raw.includes("serious") || raw.includes("focus")) return "serious";
  if (raw.includes("worried") || raw.includes("sad")) return "worried";
  if (raw.includes("angry")) return "angry";
  if (raw.includes("surprise")) return "surprised";
  if (raw.includes("think")) return "thinking";
  if (raw.includes("embarrass")) return "embarrassed";
  if (raw.includes("calm")) return "calm";
  return raw;
}

function makePreviewLines(lines, visibleAssistant, limit) {
  const source = Array.isArray(lines) && lines.length
    ? lines
    : (visibleAssistant?.text ? [{ text: visibleAssistant.text }] : []);
  return source
    .map((line) => previewLineText(line?.text || line?.display_text || line?.displayText || ""))
    .filter(Boolean)
    .slice(0, limit)
    .map((text) => ({ text }));
}

function previewLineText(value) {
  const text = String(value || "").replace(/\s+/g, " ").trim();
  const limit = looksMostlyASCII(text) ? 112 : 46;
  const chars = Array.from(text);
  if (chars.length <= limit) return text;
  return `${chars.slice(0, limit).join("").trim()}...`;
}

function looksMostlyASCII(value) {
  const chars = Array.from(value);
  if (!chars.length) return false;
  const ascii = chars.filter((char) => char.charCodeAt(0) <= 0x7f).length;
  return ascii / chars.length > 0.75;
}
