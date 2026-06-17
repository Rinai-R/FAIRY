const EXPRESSION_LABELS = {
  calm: "平静",
  soft_smile: "浅笑",
  happy: "开心",
  curious: "好奇",
  surprised: "惊讶",
  thinking: "思考",
  serious: "认真",
  worried: "担心",
  embarrassed: "害羞",
  angry: "生气",
  gentle: "温柔"
};

const MOTION_LABELS = {
  idle: "待机",
  idle_talk: "轻声讲述",
  talk: "对白",
  script: "演出",
  opening: "开场",
  lesson: "讲解",
  choice: "选择",
  challenge: "问答",
  summary: "总结",
  free_discussion: "自由讨论",
  gentle_nod: "轻轻点头"
};

export function expressionLabel(value, character) {
  const key = normalizeStateKey(value);
  if (!key) return "浅笑";
  const custom = character?.assets?.moods?.[key]?.label || character?.assets?.moods?.[key]?.name || "";
  return custom || EXPRESSION_LABELS[key] || humanizeStateKey(key);
}

export function emotionLabel(value) {
  return expressionLabel(value);
}

export function motionLabel(value) {
  const key = normalizeStateKey(value);
  if (!key) return "待机";
  return MOTION_LABELS[key] || humanizeStateKey(key);
}

function normalizeStateKey(value) {
  return String(value || "").trim().toLowerCase().replace(/[\s-]+/g, "_");
}

function humanizeStateKey(value) {
  return String(value || "")
    .split("_")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
