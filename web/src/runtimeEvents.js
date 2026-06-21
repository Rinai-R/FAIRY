const VALID_LEVELS = new Set(["info", "warn", "error"]);

export function normalizeRuntimeEvents(payload = {}) {
  const events = Array.isArray(payload) ? payload : Array.isArray(payload?.events) ? payload.events : [];
  return events
    .filter((event) => event && typeof event === "object" && String(event.message || "").trim())
    .map((event, index) => ({
      id: String(event.id || runtimeEventFallbackID(event, index)),
      session_id: String(event.session_id || ""),
      level: normalizeLogLevel(event.level),
      type: String(event.type || "runtime.event"),
      stage: String(event.stage || "runtime"),
      message: String(event.message || ""),
      detail: String(event.detail || ""),
      node_id: String(event.node_id || event.nodeID || ""),
      provider: String(event.provider || ""),
      retry_count: normalizeRetryCount(event.retry_count),
      duration_ms: normalizeDurationMS(event.duration_ms ?? event.durationMS),
      created_at: event.created_at || event.createdAt || ""
    }));
}

export function createFrontendLog(level, message, now = new Date()) {
  return {
    id: `frontend:${now.toISOString()}:${Math.random().toString(36).slice(2)}`,
    source: "frontend",
    level: normalizeLogLevel(level),
    message,
    time: now.toLocaleTimeString(),
    createdAt: now.toISOString()
  };
}

export function buildLogTimeline(frontendLogs = [], runtimeEvents = []) {
  const frontendItems = (Array.isArray(frontendLogs) ? frontendLogs : []).map((log, index) => frontendLogToTimelineItem(log, index, frontendLogs.length));
  const runtimeItems = normalizeRuntimeEvents(runtimeEvents).map(runtimeEventToTimelineItem);
  const seen = new Set();
  return [...frontendItems, ...runtimeItems]
    .filter((item) => {
      if (!item.message) return false;
      if (seen.has(item.id)) return false;
      seen.add(item.id);
      return true;
    })
    .sort(compareTimelineItems);
}

export function aggregateRuntimeEventsFromRecords(records = [], activeRuntimeEvents = [], limit = 300) {
  const events = [
    ...normalizeRuntimeEvents(activeRuntimeEvents),
    ...(Array.isArray(records) ? records.flatMap((record) => recordRuntimeEvents(record)) : [])
  ];
  const seen = new Set();
  const normalizedLimit = Number(limit);
  const max = Number.isFinite(normalizedLimit) && normalizedLimit > 0 ? Math.trunc(normalizedLimit) : events.length;
  return events
    .filter((event) => {
      if (!event.message) return false;
      if (seen.has(event.id)) return false;
      seen.add(event.id);
      return true;
    })
    .sort(compareRuntimeEvents)
    .slice(0, max);
}

export function filterLogTimeline(timeline, { level = "all", source = "all", stage = "all", query = "" } = {}) {
  const expectedLevel = normalizeLevelFilter(level);
  const expectedSource = normalizeSourceFilter(source);
  const expectedStage = normalizeStageFilter(stage);
  const needle = query.trim().toLowerCase();
  return (Array.isArray(timeline) ? timeline : []).filter((item) => {
    if (expectedLevel !== "all" && item.level !== expectedLevel) return false;
    if (expectedSource !== "all" && item.source !== expectedSource) return false;
    if (expectedStage !== "all" && item.stage !== expectedStage) return false;
    if (!needle) return true;
    return logSearchText(item).includes(needle);
  });
}

export function runtimeIssueLogs(timeline, limit = 5) {
  const issues = (Array.isArray(timeline) ? timeline : [])
    .filter((item) => item.level === "error" || item.level === "warn")
    .sort(compareIssueItems);
  const normalizedLimit = Number(limit);
  if (!Number.isFinite(normalizedLimit) || normalizedLimit <= 0) return issues;
  return issues.slice(0, Math.trunc(normalizedLimit));
}

export function recordRuntimeIssueLogs(record, limit = 6) {
  return runtimeIssueLogs(buildLogTimeline([], recordRuntimeEvents(record)), limit);
}

export function recordRuntimeTimeline(record, limit = 12) {
  const timeline = buildLogTimeline([], recordRuntimeEvents(record));
  const normalizedLimit = Number(limit);
  if (!Number.isFinite(normalizedLimit) || normalizedLimit <= 0) return timeline;
  return timeline.slice(0, Math.trunc(normalizedLimit));
}

export function recordRuntimeEvents(record) {
  return [
    ...normalizeRuntimeEvents(record?.events || []),
    ...deriveRuntimeEventsFromRecord(record)
  ];
}

export function runtimeEventMeta(log) {
  return [log.stage, log.node_id, log.provider, runtimeDurationLabel(log.duration_ms), runtimeRetryLabel(log.retry_count)].filter(Boolean).join(" · ");
}

export function runtimeEventDiagnosticText(log) {
  const detail = String(log?.detail || "").replace(/\s+/g, " ").trim();
  if (!detail) return "";
  const message = String(log?.message || "").replace(/\s+/g, " ").trim();
  if (detail === message) return "";
  return detail;
}

export function runtimeLogCopyText(log) {
  return [
    `time: ${log?.createdAt || log?.time || ""}`,
    `source: ${log?.source || ""}`,
    `level: ${log?.level || ""}`,
    log?.type ? `type: ${log.type}` : "",
    log?.stage ? `stage: ${log.stage}` : "",
    log?.node_id ? `node_id: ${log.node_id}` : "",
    log?.provider ? `provider: ${log.provider}` : "",
    log?.duration_ms ? `duration_ms: ${log.duration_ms}` : "",
    log?.retry_count ? `retry_count: ${log.retry_count}` : "",
    `message: ${log?.message || ""}`,
    log?.detail ? `detail: ${log.detail}` : ""
  ].filter(Boolean).join("\n");
}

function runtimeEventToTimelineItem(event) {
  const timestamp = parseTimestamp(event.created_at);
  const createdAt = timestamp ? new Date(timestamp).toISOString() : "";
  return {
    id: `runtime:${event.id}`,
    source: "runtime",
    level: event.level,
    type: event.type,
    stage: event.stage,
    message: event.message,
    detail: event.detail,
    node_id: event.node_id,
    provider: event.provider,
    retry_count: event.retry_count,
    duration_ms: event.duration_ms,
    createdAt,
    time: timestamp ? new Date(timestamp).toLocaleTimeString() : "--:--:--",
    timestamp: timestamp || 0
  };
}

function frontendLogToTimelineItem(log, index, total) {
  const timestamp = parseTimestamp(log?.createdAt || log?.created_at);
  const fallbackTimestamp = timestamp || total - index;
  return {
    id: String(log?.id || `frontend:${log?.createdAt || log?.time || index}:${index}`),
    source: "frontend",
    level: normalizeLogLevel(log?.level),
    message: String(log?.message || ""),
    detail: "",
    stage: "",
    node_id: "",
    provider: "",
    retry_count: 0,
    duration_ms: 0,
    createdAt: log?.createdAt || log?.created_at || "",
    time: log?.time || (timestamp ? new Date(timestamp).toLocaleTimeString() : ""),
    timestamp: fallbackTimestamp
  };
}

function normalizeLogLevel(level) {
  const normalized = String(level || "info").toLowerCase() === "warning" ? "warn" : String(level || "info").toLowerCase();
  return VALID_LEVELS.has(normalized) ? normalized : "info";
}

function normalizeLevelFilter(level) {
  if (level === "all") return "all";
  return normalizeLogLevel(level);
}

function normalizeSourceFilter(source) {
  const normalized = String(source || "all").toLowerCase();
  return normalized === "frontend" || normalized === "runtime" ? normalized : "all";
}

function normalizeStageFilter(stage) {
  const normalized = String(stage || "all").trim();
  return normalized === "" ? "all" : normalized;
}

function normalizeRetryCount(value) {
  const count = Number(value);
  return Number.isFinite(count) && count > 0 ? Math.trunc(count) : 0;
}

function normalizeDurationMS(value) {
  const duration = Number(value);
  return Number.isFinite(duration) && duration > 0 ? Math.round(duration) : 0;
}

function runtimeRetryLabel(count) {
  const normalized = normalizeRetryCount(count);
  return normalized > 0 ? `重试 ${normalized} 次` : "";
}

function runtimeDurationLabel(value) {
  const duration = normalizeDurationMS(value);
  if (!duration) return "";
  if (duration < 1000) return `耗时 ${duration}ms`;
  const seconds = duration / 1000;
  const digits = seconds < 10 ? 2 : 1;
  return `耗时 ${seconds.toFixed(digits).replace(/\.?0+$/, "")}s`;
}

function parseTimestamp(value) {
  if (!value) return 0;
  const timestamp = new Date(value).getTime();
  return Number.isFinite(timestamp) ? timestamp : 0;
}

function logSearchText(item) {
  return [
    item.source,
    item.level,
    item.type,
    item.stage,
    item.node_id,
    item.provider,
    runtimeEventSearchAliases(item),
    runtimeDurationLabel(item.duration_ms),
    item.duration_ms ? `duration_ms:${item.duration_ms}` : "",
    runtimeRetryLabel(item.retry_count),
    item.retry_count ? `retry_count:${item.retry_count}` : "",
    item.message,
    item.detail
  ].filter(Boolean).join(" ").toLowerCase();
}

function runtimeEventSearchAliases(item) {
  const type = String(item?.type || "");
  const aliases = [];
  if (type.startsWith("agent.actplan")) aliases.push("actplan", "act plan", "教学规划");
  if (type.startsWith("agent.generate_act.draft")) aliases.push("generate_act", "draft", "草稿");
  if (type.startsWith("agent.rewrite_act")) aliases.push("rewrite", "rewrite_act", "改写");
  if (type === "agent.generate_act.retry" || (type.startsWith("agent.") && type.endsWith(".retry"))) {
    aliases.push("correction", "repair", "retry", "修正重试");
  }
  return aliases.join(" ");
}

function runtimeEventFallbackID(event, index) {
  return [
    event.session_id || "",
    event.type || "",
    event.stage || "",
    event.node_id || "",
    event.provider || "",
    event.created_at || "",
    index
  ].join(":");
}

function compareTimelineItems(a, b) {
  return b.timestamp - a.timestamp
    || runtimeEventSortRank(a) - runtimeEventSortRank(b)
    || String(b.id).localeCompare(String(a.id));
}

function compareRuntimeEvents(a, b) {
  return parseTimestamp(b.created_at) - parseTimestamp(a.created_at)
    || runtimeEventSortRank(a) - runtimeEventSortRank(b)
    || String(b.id).localeCompare(String(a.id));
}

function runtimeEventSortRank(item) {
  const type = String(item?.type || "");
  if (type === "generation.failed") return 0;
  if (type === "agent.generate_act.failed") return 10;
  if (type === "agent.generate_act.retry") return 15;
  if (type === "workflow.node.failed") return 20;
  if (type === "voice.synthesize.failed") return 30;
  if (type.endsWith(".failed")) return 40;
  if (item?.level === "error") return 50;
  if (item?.level === "warn") return 60;
  return 70;
}

function compareIssueItems(a, b) {
  return issueSeverityRank(a) - issueSeverityRank(b)
    || (b.timestamp || 0) - (a.timestamp || 0)
    || runtimeEventSortRank(a) - runtimeEventSortRank(b)
    || String(b.id).localeCompare(String(a.id));
}

function issueSeverityRank(item) {
  if (item?.level === "error") return 0;
  if (item?.level === "warn") return 1;
  return 2;
}

function deriveRuntimeEventsFromRecord(record) {
  if (!record || typeof record !== "object") return [];
  const sessionID = String(record?.session?.id || "");
  const events = [];
  const generation = record.generation || {};
  const generationError = String(generation.error || "").trim();
  if (generationError) {
    events.push({
      id: `derived:${sessionID}:generation.failed`,
      session_id: sessionID,
      level: "error",
      type: "generation.failed",
      stage: "generation",
      message: generationError,
      detail: String(generation.request?.topic || record.scene?.title || ""),
      created_at: generation.completed_at || record.updated_at || generation.started_at || ""
    });
  }

  for (const node of Array.isArray(record.workflow?.nodes) ? record.workflow.nodes : []) {
    const nodeID = String(node?.id || "");
    const nodeTitle = String(node?.title || node?.summary || nodeID || "剧情节点").trim();
    if (node?.prepare_error) {
      events.push({
        id: `derived:${sessionID}:${nodeID}:workflow.node.failed`,
        session_id: sessionID,
        level: "error",
        type: "workflow.node.failed",
        stage: "workflow",
        node_id: nodeID,
        message: `${nodeTitle} 生成失败：${node.prepare_error}`,
        created_at: record.updated_at || generation.completed_at || ""
      });
    } else if (node?.status === "error") {
      events.push({
        id: `derived:${sessionID}:${nodeID}:workflow.node.failed`,
        session_id: sessionID,
        level: "error",
        type: "workflow.node.failed",
        stage: "workflow",
        node_id: nodeID,
        message: `${nodeTitle} 生成失败`,
        created_at: record.updated_at || generation.completed_at || ""
      });
    }
    if (node?.voice_status === "error") {
      events.push({
        id: `derived:${sessionID}:${nodeID}:voice.synthesize.failed`,
        session_id: sessionID,
        level: "error",
        type: "voice.synthesize.failed",
        stage: "voice",
        node_id: nodeID,
        message: `${nodeTitle} 语音生成失败`,
        created_at: record.updated_at || generation.completed_at || ""
      });
    }
    for (const [index, line] of (Array.isArray(node?.lines) ? node.lines : []).entries()) {
      if (!line?.audio_error && line?.audio_status !== "error") continue;
      events.push({
        id: `derived:${sessionID}:${nodeID}:line-${index + 1}:voice.synthesize.failed`,
        session_id: sessionID,
        level: "error",
        type: "voice.synthesize.failed",
        stage: "voice",
        node_id: nodeID,
        message: line.audio_error ? `${nodeTitle} 第 ${index + 1} 句语音生成失败：${line.audio_error}` : `${nodeTitle} 第 ${index + 1} 句语音生成失败`,
        created_at: record.updated_at || generation.completed_at || ""
      });
    }
  }
  return normalizeRuntimeEvents(events);
}
