import { workflowNodeReady } from "./views/workflowReadiness.js";

export const HISTORY_STATUS = Object.freeze({
  DRAFT: "draft",
  GENERATING: "generating",
  PLAYABLE: "playable",
  COMPLETED: "completed",
  FAILED: "failed"
});

export function recordGenerationStatus(record) {
  const generationStatus = rawGenerationStatus(record);
  if (generationStatus === "failed") return HISTORY_STATUS.FAILED;
  if (generationStatus === "ready") {
    if (workflowArchiveFailed(record?.workflow)) return HISTORY_STATUS.FAILED;
    if (workflowArchiveComplete(record?.workflow)) return HISTORY_STATUS.COMPLETED;
    return recordPlayable(record) ? HISTORY_STATUS.PLAYABLE : HISTORY_STATUS.GENERATING;
  }
  if (generationStatus === "generating" || generationStatus === "preparing") {
    if (workflowArchiveFailed(record?.workflow)) return HISTORY_STATUS.FAILED;
    return recordPlayable(record) ? HISTORY_STATUS.PLAYABLE : HISTORY_STATUS.GENERATING;
  }
  return recordPlayable(record) ? HISTORY_STATUS.PLAYABLE : HISTORY_STATUS.DRAFT;
}

export function recordHasActiveGeneration(record) {
  const status = rawGenerationStatus(record);
  return status === "generating" || status === "preparing";
}

export function recordPlayable(record) {
  return hasPlayableWorkflow(record?.workflow, record?.scene, record?.session);
}

export function recordReadinessTimes(record) {
  const playableAt = firstPlayableAt(record);
  const completedAt = recordGenerationStatus(record) === HISTORY_STATUS.COMPLETED ? workflowCompletedAt(record) : "";
  return {
    playable_at: playableAt,
    completed_at: completedAt,
    saved_at: isoTime(record?.updated_at)
  };
}

export function recordPrimaryHistoryTime(record) {
  const status = recordGenerationStatus(record);
  const times = recordReadinessTimes(record);
  if (status === HISTORY_STATUS.PLAYABLE && times.playable_at) {
    return { label: "可演出", value: times.playable_at };
  }
  if (status === HISTORY_STATUS.COMPLETED && times.completed_at) {
    return { label: "完成", value: times.completed_at };
  }
  if (status === HISTORY_STATUS.GENERATING) {
    return { label: times.playable_at ? "可演出" : "更新", value: times.playable_at || times.saved_at };
  }
  if (status === HISTORY_STATUS.FAILED) {
    return { label: "失败", value: times.saved_at };
  }
  return { label: "保存", value: times.saved_at };
}

export function hasPlayableWorkflow(workflow, scene, session) {
  if (!session?.id || scene?.id === "lesson-draft") return false;
  const nodes = Array.isArray(workflow?.nodes) ? workflow.nodes : [];
  return nodes.some(nodePlayable);
}

export function workflowArchiveComplete(workflow) {
  const nodes = Array.isArray(workflow?.nodes) ? workflow.nodes : [];
  if (!nodes.length || workflow?.preparing || workflow?.pending_node_id) return false;
  const byID = new Map(nodes.filter((node) => node?.id).map((node) => [node.id, node]));
  for (const node of nodes) {
    if (!workflowNodeReady(node)) return false;
  }
  for (const node of nodes) {
    if (!node?.id || node.free_discussion || node.kind === "free_discussion") continue;
    if (node.next_node_id && !workflowNodeReady(byID.get(node.next_node_id))) return false;
    if (!node.next_node_id && ["continue", "summarize", "free_discussion"].includes(String(node.decision || "").trim())) return false;
    for (const choice of Array.isArray(node.choices) ? node.choices : []) {
      const targetID = String(choice?.target_node_id || "").trim();
      if (!targetID || !workflowNodeReady(byID.get(targetID))) return false;
    }
  }
  return true;
}

export function workflowArchiveFailed(workflow) {
  return Boolean(workflowArchiveFailureMessage(workflow));
}

export function recordFailureMessage(record) {
  return String(record?.generation?.error || workflowArchiveFailureMessage(record?.workflow) || "").trim();
}

export function workflowArchiveFailureMessage(workflow) {
  const nodes = Array.isArray(workflow?.nodes) ? workflow.nodes : [];
  for (const node of nodes) {
    const nodeLabel = safeHistoryText(node?.title || node?.summary || node?.id || "剧情节点");
    if (node?.prepare_error) return node.prepare_error;
    if (node?.voice_status === "error") return `${nodeLabel} 语音生成失败`;
    if (node?.status === "error") return `${nodeLabel} 生成失败`;
    for (const [index, line] of (Array.isArray(node?.lines) ? node.lines : []).entries()) {
      if (line?.audio_error) return `${nodeLabel} 第 ${index + 1} 句语音生成失败：${line.audio_error}`;
      if (line?.audio_status === "error") return `${nodeLabel} 第 ${index + 1} 句语音生成失败`;
    }
  }
  return "";
}

export function historyStatusLabel(status) {
  switch (status) {
    case HISTORY_STATUS.PLAYABLE: return "可演出";
    case HISTORY_STATUS.COMPLETED:
    case "ready": return "已完成";
    case HISTORY_STATUS.GENERATING: return "生成中";
    case HISTORY_STATUS.FAILED: return "失败";
    default: return "草稿";
  }
}

export function historyStatusClass(status) {
  switch (status) {
    case HISTORY_STATUS.PLAYABLE: return "playable";
    case HISTORY_STATUS.COMPLETED:
    case "ready": return "ok";
    case HISTORY_STATUS.GENERATING: return "generating";
    case HISTORY_STATUS.FAILED: return "failed";
    default: return "draft";
  }
}

export function historyStatusColor(status) {
  switch (status) {
    case HISTORY_STATUS.PLAYABLE: return "var(--accent)";
    case HISTORY_STATUS.COMPLETED:
    case "ready": return "var(--ok)";
    case HISTORY_STATUS.GENERATING: return "var(--brand-deep)";
    case HISTORY_STATUS.FAILED: return "var(--bad)";
    default: return "var(--warn)";
  }
}

export function historyStatusDescription(status) {
  switch (status) {
    case HISTORY_STATUS.PLAYABLE:
      return "已有可播放章节，可以先进入演出；完整章节和分支仍会继续准备。";
    case HISTORY_STATUS.GENERATING:
      return "剧情仍在后台准备首个可播放章节，请稍后刷新或等待自动更新。";
    case HISTORY_STATUS.FAILED:
      return "这条生成任务失败了，记录中保留了错误信息，修正配置后可以重新发起。";
    case HISTORY_STATUS.COMPLETED:
    case "ready":
      return "完整章节脚本已闭合，可直接进入剧情演出；不再需要的记录可以删除。";
    default:
      return "草稿尚未生成可演出的章节，可继续补全或删除。";
  }
}

function rawGenerationStatus(record) {
  return String(record?.generation?.status || "").trim();
}

function firstPlayableAt(record) {
  if (!recordPlayable(record)) return "";
  const historyTimes = workflowHistoryTimes(record?.workflow);
  const candidates = [];
  for (const node of Array.isArray(record?.workflow?.nodes) ? record.workflow.nodes : []) {
    if (!nodePlayable(node)) continue;
    const readyAt = isoTime(node?.ready_at) || historyTimes.get(node.id) || "";
    if (readyAt) candidates.push(readyAt);
  }
  return earliestISO(candidates);
}

function workflowCompletedAt(record) {
  const historyTimes = workflowHistoryTimes(record?.workflow);
  const candidates = [];
  for (const node of Array.isArray(record?.workflow?.nodes) ? record.workflow.nodes : []) {
    if (!workflowNodeReady(node)) continue;
    const readyAt = isoTime(node?.ready_at) || historyTimes.get(node.id) || "";
    if (readyAt) candidates.push(readyAt);
  }
  return latestISO(candidates) || isoTime(record?.updated_at);
}

function nodePlayable(node) {
  if (!node?.id || node.kind === "draft") return false;
  if (!workflowNodeReady(node)) return false;
  if (Array.isArray(node.lines) && node.lines.some((line) => String(line?.text || "").trim())) return true;
  if (String(node.line || "").trim()) return true;
  if (Array.isArray(node.choices) && node.choices.length > 0) return true;
  return Boolean(node.free_discussion || node.kind === "free_discussion");
}

function workflowHistoryTimes(workflow) {
  const out = new Map();
  for (const item of Array.isArray(workflow?.history) ? workflow.history : []) {
    const nodeID = String(item?.node_id || item?.id || "").trim();
    const occurredAt = isoTime(item?.occurred_at);
    if (!nodeID || !occurredAt) continue;
    const previous = out.get(nodeID);
    if (!previous || occurredAt < previous) out.set(nodeID, occurredAt);
  }
  return out;
}

function earliestISO(values) {
  return values.filter(Boolean).sort()[0] || "";
}

function latestISO(values) {
  const sorted = values.filter(Boolean).sort();
  return sorted.at(-1) || "";
}

function isoTime(value) {
  if (!value) return "";
  const time = new Date(value);
  if (Number.isNaN(time.getTime())) return "";
  return time.toISOString();
}

function safeHistoryText(value) {
  return String(value || "")
    .replace(/教学工作流/g, "教学剧情")
    .replace(/工作流/g, "剧情")
    .replace(/脚本节点/g, "剧情段落")
    .replace(/自由讨论节点/g, "自由讨论")
    .replace(/\s+/g, " ")
    .trim();
}
