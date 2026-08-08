import { getToken } from "./api";
import type { MetricsTrendPoint } from "./metricsTrend";

export const MAX_VISIBLE_LOGS = 500;
export const MAX_PENDING_LOGS = 500;
const MAX_SSE_LINE = 64 * 1024;
const MAX_SSE_FRAME = 256 * 1024;

export type LogLevel = "debug" | "info" | "warn" | "error";

export type LogField = { key: string; value: string; truncated: boolean };

export type LogEntry = {
  sequence: number;
  timestampUnixMs: number;
  level: LogLevel;
  logger: string;
  message: string;
  messageTruncated: boolean;
  fields: LogField[];
  fieldsTruncated: boolean;
};

export type RouteMetrics = {
  method: string;
  route: string;
  longLived: boolean;
  requestCount: number;
  errorCount: number;
  totalDurationMs: number;
  maxDurationMs: number;
};

export type UsageLane = {
  lane: string;
  inputTokens: number;
  outputTokens: number;
  cachedInputTokens: number;
  cachedObservedInputTokens: number;
  cacheWriteTokens: number;
  callCount: number;
};

export type UsageTurn = {
  conversationId: string;
  turnId: string;
  characterId: string;
  createdAtUnixMs: number;
  status: string;
  lanes: UsageLane[];
};

export type MessageLatency = {
  observations: number;
  totalDurationMs: number;
  maxDurationMs: number;
};

export type MessageTrace = {
  traceId: string;
  messageId: string;
  source: string;
  conversationId: string;
  turnId: string;
  status: string;
  receivedAtUnixMs: number;
  decisionAtUnixMs: number;
  turnStartedAtUnixMs: number;
  firstBeatAtUnixMs: number;
  completedAtUnixMs: number;
  totalDurationMs: number;
};

export type TraceSpan = {
  spanId: string;
  parentSpanId: string;
  operation: string;
  category: string;
  status: string;
  startedAtUnixMs: number;
  endedAtUnixMs: number;
  durationMs: number;
  attributes: Record<string, string>;
};

export type TraceDetail = {
  traceId: string;
  messageId: string;
  conversationId: string;
  turnId: string;
  source: string;
  status: string;
  startedAtUnixMs: number;
  endedAtUnixMs: number;
  durationMs: number;
  droppedSpanCount: number;
  truncated: boolean;
  spans: TraceSpan[];
};

export type MetricsSnapshot = {
  generatedAtUnixMs: number;
  messagesAvailable: boolean;
  process: { uptimeSeconds: number; goVersion: string; goroutines: number; heapAllocBytes: number };
  http: {
    inFlight: number;
    total: number;
    status2xx: number;
    status4xx: number;
    status5xx: number;
    routes: RouteMetrics[];
  };
  logs: {
    retainedEntries: number;
    droppedEntries: number;
    activeSubscribers: number;
    slowSubscriberDisconnects: number;
  };
  messages: {
    received: number;
    sent: number;
    directReceived: number;
    ambientReceived: number;
    completed: number;
    failed: number;
    interrupted: number;
    silent: number;
    active: number;
    droppedEvents: number;
    latencies: {
      receiveToDecision: MessageLatency;
      receiveToTurn: MessageLatency;
      turnToFirstBeat: MessageLatency;
      turnToCompleted: MessageLatency;
      receiveToFirstBeat: MessageLatency;
      receiveToCompleted: MessageLatency;
    };
    recent: MessageTrace[];
  };
  runtime: {
    activeBackgroundJobs: number;
    eventSubscribers: number;
    compaction: CompactionStats;
    experience: ExperienceStats;
  };
  usage: { overall: UsageLane[]; turns: UsageTurn[]; turnCount: number; truncated: boolean };
  history: MetricsTrendPoint[];
};

export type ExperienceStats = {
  learning: { enqueued: number; dropped: number; succeeded: number; failed: number };
  feedback: { registered: number; dropped: number; succeeded: number; failed: number };
  cacheIdentityVersion: string;
};

export type CompactionStats = {
  l1Applied: number;
  l2Applied: number;
  l3Applied: number;
  failed: number;
};

type SSEEvent = { id: string; event: string; data: string };

export function parseLogEntry(value: unknown): LogEntry {
  const record = asRecord(value, "log entry");
  const level = requiredString(record, "level") as LogLevel;
  if (!["debug", "info", "warn", "error"].includes(level)) {
    throw new Error(`未知日志级别：${level}`);
  }
  const fieldsValue = record.fields;
  if (!Array.isArray(fieldsValue)) throw new Error("日志 fields 必须是数组");
  return {
    sequence: requiredPositiveInteger(record, "sequence"),
    timestampUnixMs: requiredPositiveInteger(record, "timestampUnixMs"),
    level,
    logger: requiredString(record, "logger"),
    message: requiredString(record, "message"),
    messageTruncated: requiredBoolean(record, "messageTruncated"),
    fields: fieldsValue.map((field) => {
      const item = asRecord(field, "log field");
      return {
        key: requiredString(item, "key"),
        value: requiredString(item, "value"),
        truncated: requiredBoolean(item, "truncated"),
      };
    }),
    fieldsTruncated: requiredBoolean(record, "fieldsTruncated"),
  };
}

export function parseMetrics(value: unknown): MetricsSnapshot {
  const root = asRecord(value, "metrics");
  const process = asRecord(root.process, "process metrics");
  const http = asRecord(root.http, "http metrics");
  const logs = asRecord(root.logs, "log metrics");
  const messagesAvailable = root.messages !== undefined;
  const messages = messagesAvailable ? asRecord(root.messages, "message metrics") : emptyMessageMetrics();
  const messageLatencies = asRecord(messages.latencies, "message latency metrics");
  const runtime = asRecord(root.runtime, "runtime metrics");
  const agentLoop = asRecord(runtime.agentLoop, "agent loop metrics");
  const compaction = asRecord(agentLoop.compaction, "context compaction metrics");
  const usage = asRecord(root.usage, "usage metrics");
  const history = root.history === undefined ? [] : root.history;
  if (!Array.isArray(http.routes)) throw new Error("metrics.http.routes 必须是数组");
  if (!Array.isArray(messages.recent)) throw new Error("metrics.messages.recent 必须是数组");
  if (!Array.isArray(usage.overall) || !Array.isArray(usage.turns)) {
    throw new Error("metrics.usage 缺少数组字段");
  }
  if (!Array.isArray(history)) throw new Error("metrics.history 必须是数组");
  return {
    generatedAtUnixMs: requiredPositiveInteger(root, "generatedAtUnixMs"),
    messagesAvailable,
    process: {
      uptimeSeconds: requiredNonNegativeInteger(process, "uptimeSeconds"),
      goVersion: requiredString(process, "goVersion"),
      goroutines: requiredNonNegativeInteger(process, "goroutines"),
      heapAllocBytes: requiredNonNegativeInteger(process, "heapAllocBytes"),
    },
    http: {
      inFlight: requiredNonNegativeInteger(http, "inFlight"),
      total: requiredNonNegativeInteger(http, "total"),
      status2xx: requiredNonNegativeInteger(http, "status2xx"),
      status4xx: requiredNonNegativeInteger(http, "status4xx"),
      status5xx: requiredNonNegativeInteger(http, "status5xx"),
      routes: http.routes.map(parseRouteMetrics),
    },
    logs: {
      retainedEntries: requiredNonNegativeInteger(logs, "retainedEntries"),
      droppedEntries: requiredNonNegativeInteger(logs, "droppedEntries"),
      activeSubscribers: requiredNonNegativeInteger(logs, "activeSubscribers"),
      slowSubscriberDisconnects: requiredNonNegativeInteger(logs, "slowSubscriberDisconnects"),
    },
    messages: {
      received: requiredNonNegativeInteger(messages, "received"),
      sent: requiredNonNegativeInteger(messages, "sent"),
      directReceived: requiredNonNegativeInteger(messages, "directReceived"),
      ambientReceived: requiredNonNegativeInteger(messages, "ambientReceived"),
      completed: requiredNonNegativeInteger(messages, "completed"),
      failed: requiredNonNegativeInteger(messages, "failed"),
      interrupted: requiredNonNegativeInteger(messages, "interrupted"),
      silent: requiredNonNegativeInteger(messages, "silent"),
      active: requiredNonNegativeInteger(messages, "active"),
      droppedEvents: requiredNonNegativeInteger(messages, "droppedEvents"),
      latencies: {
        receiveToDecision: parseMessageLatency(messageLatencies.receiveToDecision),
        receiveToTurn: parseMessageLatency(messageLatencies.receiveToTurn),
        turnToFirstBeat: parseMessageLatency(messageLatencies.turnToFirstBeat),
        turnToCompleted: parseMessageLatency(messageLatencies.turnToCompleted),
        receiveToFirstBeat: parseMessageLatency(messageLatencies.receiveToFirstBeat),
        receiveToCompleted: parseMessageLatency(messageLatencies.receiveToCompleted),
      },
      recent: messages.recent.map(parseMessageTrace),
    },
    runtime: {
      activeBackgroundJobs: requiredNonNegativeInteger(runtime, "activeBackgroundJobs"),
      eventSubscribers: requiredNonNegativeInteger(runtime, "eventSubscribers"),
      compaction: {
        l1Applied: requiredNonNegativeInteger(compaction, "l1Applied"),
        l2Applied: requiredNonNegativeInteger(compaction, "l2Applied"),
        l3Applied: requiredNonNegativeInteger(compaction, "l3Applied"),
        failed: requiredNonNegativeInteger(compaction, "failed"),
      },
      experience: parseExperienceStats(runtime.experience),
    },
    usage: {
      overall: usage.overall.map(parseUsageLane),
      turns: usage.turns.map(parseUsageTurn),
      turnCount: requiredNonNegativeInteger(usage, "turnCount"),
      truncated: requiredBoolean(usage, "truncated"),
    },
    history: history.map(parseMetricHistoryPoint),
  };
}

function parseMetricHistoryPoint(value: unknown): MetricsTrendPoint {
  const point = asRecord(value, "metric history point");
  return {
    timestampUnixMs: requiredPositiveInteger(point, "timestampUnixMs"),
    processStartedAtUnixMs: requiredPositiveInteger(point, "processStartedAtUnixMs"),
    httpTotal: requiredNonNegativeInteger(point, "httpTotal"),
    httpInFlight: requiredNonNegativeInteger(point, "httpInFlight"),
    httpStatus4xx: requiredNonNegativeInteger(point, "httpStatus4xx"),
    httpStatus5xx: requiredNonNegativeInteger(point, "httpStatus5xx"),
    messagesReceived: requiredNonNegativeInteger(point, "messagesReceived"),
    messagesSent: requiredNonNegativeInteger(point, "messagesSent"),
    messagesActive: requiredNonNegativeInteger(point, "messagesActive"),
    messagesFailed: requiredNonNegativeInteger(point, "messagesFailed"),
    inputTokens: requiredNonNegativeInteger(point, "inputTokens"),
    cachedInputTokens: requiredNonNegativeInteger(point, "cachedInputTokens"),
    outputTokens: requiredNonNegativeInteger(point, "outputTokens"),
    modelCalls: requiredNonNegativeInteger(point, "modelCalls"),
    learningEnqueued: optionalNonNegativeInteger(point, "learningEnqueued"),
    learningSucceeded: optionalNonNegativeInteger(point, "learningSucceeded"),
    learningFailed: optionalNonNegativeInteger(point, "learningFailed"),
    learningDropped: optionalNonNegativeInteger(point, "learningDropped"),
    feedbackRegistered: optionalNonNegativeInteger(point, "feedbackRegistered"),
    feedbackSucceeded: optionalNonNegativeInteger(point, "feedbackSucceeded"),
    feedbackFailed: optionalNonNegativeInteger(point, "feedbackFailed"),
    feedbackDropped: optionalNonNegativeInteger(point, "feedbackDropped"),
    compactionL1Applied: optionalNonNegativeInteger(point, "compactionL1Applied"),
    compactionL2Applied: optionalNonNegativeInteger(point, "compactionL2Applied"),
    compactionL3Applied: optionalNonNegativeInteger(point, "compactionL3Applied"),
    compactionFailed: optionalNonNegativeInteger(point, "compactionFailed"),
    goroutines: requiredNonNegativeInteger(point, "goroutines"),
    backgroundJobs: requiredNonNegativeInteger(point, "backgroundJobs"),
    eventSubscribers: requiredNonNegativeInteger(point, "eventSubscribers"),
    logSubscribers: requiredNonNegativeInteger(point, "logSubscribers"),
    heapMiB: requiredNonNegativeNumber(point, "heapMiB"),
  };
}

function parseExperienceStats(value: unknown): ExperienceStats {
  const experience = asRecord(value, "experience metrics");
  const learning = asRecord(experience.learning, "experience learning metrics");
  const feedback = asRecord(experience.feedback, "experience feedback metrics");
  return {
    learning: {
      enqueued: requiredNonNegativeInteger(learning, "enqueued"),
      dropped: requiredNonNegativeInteger(learning, "dropped"),
      succeeded: requiredNonNegativeInteger(learning, "succeeded"),
      failed: requiredNonNegativeInteger(learning, "failed"),
    },
    feedback: {
      registered: requiredNonNegativeInteger(feedback, "registered"),
      dropped: requiredNonNegativeInteger(feedback, "dropped"),
      succeeded: requiredNonNegativeInteger(feedback, "succeeded"),
      failed: requiredNonNegativeInteger(feedback, "failed"),
    },
    cacheIdentityVersion: requiredString(experience, "cacheIdentityVersion"),
  };
}

function emptyMessageMetrics(): Record<string, unknown> {
  const latency = { observations: 0, totalDurationMs: 0, maxDurationMs: 0 };
  return {
    received: 0,
    sent: 0,
    directReceived: 0,
    ambientReceived: 0,
    completed: 0,
    failed: 0,
    interrupted: 0,
    silent: 0,
    active: 0,
    droppedEvents: 0,
    latencies: {
      receiveToDecision: latency,
      receiveToTurn: latency,
      turnToFirstBeat: latency,
      turnToCompleted: latency,
      receiveToFirstBeat: latency,
      receiveToCompleted: latency,
    },
    recent: [],
  };
}

function parseMessageLatency(value: unknown): MessageLatency {
  const metric = asRecord(value, "message latency");
  return {
    observations: requiredNonNegativeInteger(metric, "observations"),
    totalDurationMs: requiredNonNegativeInteger(metric, "totalDurationMs"),
    maxDurationMs: requiredNonNegativeInteger(metric, "maxDurationMs"),
  };
}

function parseMessageTrace(value: unknown): MessageTrace {
  const trace = asRecord(value, "message trace");
  return {
    traceId: requiredString(trace, "traceId"),
    messageId: optionalString(trace, "messageId"),
    source: requiredString(trace, "source"),
    conversationId: requiredString(trace, "conversationId"),
    turnId: optionalString(trace, "turnId"),
    status: requiredString(trace, "status"),
    receivedAtUnixMs: requiredPositiveInteger(trace, "receivedAtUnixMs"),
    decisionAtUnixMs: optionalNonNegativeInteger(trace, "decisionAtUnixMs"),
    turnStartedAtUnixMs: optionalNonNegativeInteger(trace, "turnStartedAtUnixMs"),
    firstBeatAtUnixMs: optionalNonNegativeInteger(trace, "firstBeatAtUnixMs"),
    completedAtUnixMs: optionalNonNegativeInteger(trace, "completedAtUnixMs"),
    totalDurationMs: optionalNonNegativeInteger(trace, "totalDurationMs"),
  };
}

export function parseTraceDetail(value: unknown): TraceDetail {
  const trace = asRecord(value, "trace detail");
  if (!Array.isArray(trace.spans)) throw new Error("trace spans 必须是数组");
  const detail: TraceDetail = {
    traceId: requiredString(trace, "traceId"),
    messageId: optionalString(trace, "messageId"),
    conversationId: requiredString(trace, "conversationId"),
    turnId: optionalString(trace, "turnId"),
    source: requiredString(trace, "source"),
    status: requiredString(trace, "status"),
    startedAtUnixMs: requiredPositiveInteger(trace, "startedAtUnixMs"),
    endedAtUnixMs: optionalNonNegativeInteger(trace, "endedAtUnixMs"),
    durationMs: requiredNonNegativeInteger(trace, "durationMs"),
    droppedSpanCount: requiredNonNegativeInteger(trace, "droppedSpanCount"),
    truncated: requiredBoolean(trace, "truncated"),
    spans: trace.spans.map(parseTraceSpan),
  };
  if (detail.endedAtUnixMs > 0) {
    if (detail.endedAtUnixMs < detail.startedAtUnixMs || detail.durationMs !== detail.endedAtUnixMs - detail.startedAtUnixMs) {
      throw new Error("trace 起止时间与耗时不一致");
    }
  }
  validateTraceTree(detail.spans);
  return detail;
}

export function parseTraceSearch(value: unknown): { messageId: string; traces: MessageTrace[] } {
  const result = asRecord(value, "trace search");
  if (!Array.isArray(result.traces)) throw new Error("trace search traces 必须是数组");
  return {
    messageId: requiredString(result, "messageId"),
    traces: result.traces.map(parseMessageTrace),
  };
}

function parseTraceSpan(value: unknown): TraceSpan {
  const span = asRecord(value, "trace span");
  const rawAttributes = asRecord(span.attributes, "trace span attributes");
  const attributes: Record<string, string> = {};
  for (const [key, item] of Object.entries(rawAttributes)) {
    if (typeof item !== "string") throw new Error(`trace span attribute ${key} 必须是 string`);
    attributes[key] = item;
  }
  const parsed: TraceSpan = {
    spanId: requiredString(span, "spanId"),
    parentSpanId: optionalString(span, "parentSpanId"),
    operation: requiredString(span, "operation"),
    category: requiredString(span, "category"),
    status: requiredString(span, "status"),
    startedAtUnixMs: requiredPositiveInteger(span, "startedAtUnixMs"),
    endedAtUnixMs: optionalNonNegativeInteger(span, "endedAtUnixMs"),
    durationMs: requiredNonNegativeInteger(span, "durationMs"),
    attributes,
  };
  if (parsed.endedAtUnixMs > 0) {
    if (parsed.endedAtUnixMs < parsed.startedAtUnixMs || parsed.durationMs !== parsed.endedAtUnixMs - parsed.startedAtUnixMs) {
      throw new Error(`span ${parsed.spanId} 起止时间与耗时不一致`);
    }
  } else if (parsed.status !== "running") {
    throw new Error(`span ${parsed.spanId} 缺少结束时间`);
  }
  return parsed;
}

function validateTraceTree(spans: TraceSpan[]) {
  const byID = new Map<string, TraceSpan>();
  for (const span of spans) {
    if (byID.has(span.spanId)) throw new Error(`重复 spanId：${span.spanId}`);
    byID.set(span.spanId, span);
  }
  for (const span of spans) {
    if (span.parentSpanId && !byID.has(span.parentSpanId)) {
      throw new Error(`span ${span.spanId} 的 parentSpanId 不存在`);
    }
    const visited = new Set<string>();
    let current: TraceSpan | undefined = span;
    while (current?.parentSpanId) {
      if (visited.has(current.spanId)) throw new Error(`span ${span.spanId} 存在父子环`);
      visited.add(current.spanId);
      current = byID.get(current.parentSpanId);
    }
  }
}

export class SSEParser {
  private buffer = "";

  push(chunk: string): SSEEvent[] {
    this.buffer += chunk;
    if (this.buffer.length > MAX_SSE_FRAME && !findFrameBoundary(this.buffer)) {
      throw new Error("SSE frame 超过 256 KiB");
    }
    const events: SSEEvent[] = [];
    let boundary = findFrameBoundary(this.buffer);
    while (boundary) {
      const frame = this.buffer.slice(0, boundary.index);
      this.buffer = this.buffer.slice(boundary.index + boundary.length);
      if (frame.length > MAX_SSE_FRAME) throw new Error("SSE frame 超过 256 KiB");
      if (frame !== "") events.push(parseSSEFrame(frame));
      boundary = findFrameBoundary(this.buffer);
    }
    return events;
  }

  finish(): SSEEvent[] {
    const events = this.push("");
    if (this.buffer.length > 0) throw new Error("SSE stream 以不完整 frame 结束");
    return events;
  }
}

export function mergeVisibleLogs(current: LogEntry[], incoming: LogEntry[], max = MAX_VISIBLE_LOGS) {
  const bySequence = new Map<number, LogEntry>();
  for (const entry of [...current, ...incoming]) bySequence.set(entry.sequence, entry);
  const ordered = [...bySequence.values()].sort((a, b) => a.sequence - b.sequence);
  const dropped = Math.max(0, ordered.length - max);
  return { entries: ordered.slice(-max), dropped };
}

export function appendPendingLogs(current: LogEntry[], incoming: LogEntry[], max = MAX_PENDING_LOGS) {
  return mergeVisibleLogs(current, incoming, max);
}

export async function followLogs(options: {
  level: LogLevel;
  loggerPrefix: string;
  signal: AbortSignal;
  onReady: () => void;
  onEntry: (entry: LogEntry) => void;
  fetchImpl?: typeof fetch;
}) {
  const values = new URLSearchParams({ level: options.level });
  if (options.loggerPrefix) values.set("logger", options.loggerPrefix);
  const headers = new Headers();
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const response = await (options.fetchImpl ?? fetch)(`/v1/logs/stream?${values}`, {
    headers,
    signal: options.signal,
  });
  if (!response.ok) throw new Error(await responseError(response));
  if (!response.headers.get("Content-Type")?.startsWith("text/event-stream")) {
    throw new Error("日志流响应不是 text/event-stream");
  }
  if (!response.body) throw new Error("日志流响应缺少 body");

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  const parser = new SSEParser();
  let ready = false;
  try {
    while (true) {
      const part = await reader.read();
      if (part.done) break;
      for (const event of parser.push(decoder.decode(part.value, { stream: true }))) {
        if (event.event === "ready") {
          ready = true;
          options.onReady();
          continue;
        }
        if (!ready || event.event !== "log") throw new Error(`未知日志 SSE event：${event.event}`);
        options.onEntry(parseLogEntry(JSON.parse(event.data) as unknown));
      }
    }
    for (const event of parser.push(decoder.decode())) {
      if (event.event === "log") options.onEntry(parseLogEntry(JSON.parse(event.data) as unknown));
    }
    parser.finish();
  } finally {
    reader.releaseLock();
  }
  if (!options.signal.aborted) throw new Error("日志流已断开");
}

function parseRouteMetrics(value: unknown): RouteMetrics {
  const route = asRecord(value, "route metrics");
  return {
    method: requiredString(route, "method"),
    route: requiredString(route, "route"),
    longLived: route.longLived === undefined ? false : requiredBoolean(route, "longLived"),
    requestCount: requiredNonNegativeInteger(route, "requestCount"),
    errorCount: requiredNonNegativeInteger(route, "errorCount"),
    totalDurationMs: requiredNonNegativeInteger(route, "totalDurationMs"),
    maxDurationMs: requiredNonNegativeInteger(route, "maxDurationMs"),
  };
}

function parseUsageLane(value: unknown): UsageLane {
  const lane = asRecord(value, "usage lane");
  return {
    lane: requiredString(lane, "lane"),
    inputTokens: requiredNonNegativeInteger(lane, "inputTokens"),
    outputTokens: requiredNonNegativeInteger(lane, "outputTokens"),
    cachedInputTokens: requiredNonNegativeInteger(lane, "cachedInputTokens"),
    cachedObservedInputTokens: requiredNonNegativeInteger(lane, "cachedObservedInputTokens"),
    cacheWriteTokens: requiredNonNegativeInteger(lane, "cacheWriteTokens"),
    callCount: requiredNonNegativeInteger(lane, "callCount"),
  };
}

function parseUsageTurn(value: unknown): UsageTurn {
  const turn = asRecord(value, "usage turn");
  if (!Array.isArray(turn.lanes)) throw new Error("usage turn lanes 必须是数组");
  return {
    conversationId: requiredString(turn, "conversationId"),
    turnId: requiredString(turn, "turnId"),
    characterId: requiredString(turn, "characterId"),
    createdAtUnixMs: requiredPositiveInteger(turn, "createdAtUnixMs"),
    status: requiredString(turn, "status"),
    lanes: turn.lanes.map(parseUsageLane),
  };
}

function parseSSEFrame(frame: string): SSEEvent {
  let id = "";
  let event = "";
  const data: string[] = [];
  for (const line of frame.split(/\r?\n/)) {
    if (line.length > MAX_SSE_LINE) throw new Error("SSE line 超过 64 KiB");
    if (line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "id") id = value;
    if (field === "event") event = value;
    if (field === "data") data.push(value);
  }
  if (!event || data.length === 0) throw new Error("SSE frame 缺少 event 或 data");
  return { id, event, data: data.join("\n") };
}

function findFrameBoundary(value: string): { index: number; length: number } | null {
  const match = /\r?\n\r?\n/.exec(value);
  return match ? { index: match.index, length: match[0].length } : null;
}

function asRecord(value: unknown, name: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${name} 必须是 object`);
  }
  return value as Record<string, unknown>;
}

function requiredString(record: Record<string, unknown>, key: string): string {
  if (typeof record[key] !== "string") throw new Error(`${key} 必须是 string`);
  return record[key];
}

function requiredBoolean(record: Record<string, unknown>, key: string): boolean {
  if (typeof record[key] !== "boolean") throw new Error(`${key} 必须是 boolean`);
  return record[key];
}

function optionalString(record: Record<string, unknown>, key: string): string {
  if (record[key] === undefined) return "";
  return requiredString(record, key);
}

function optionalNonNegativeInteger(record: Record<string, unknown>, key: string): number {
  if (record[key] === undefined) return 0;
  return requiredNonNegativeInteger(record, key);
}

function requiredPositiveInteger(record: Record<string, unknown>, key: string): number {
  const value = requiredNonNegativeInteger(record, key);
  if (value === 0) throw new Error(`${key} 必须大于 0`);
  return value;
}

function requiredNonNegativeInteger(record: Record<string, unknown>, key: string): number {
  const value = record[key];
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    throw new Error(`${key} 必须是非负安全整数`);
  }
  return value;
}

function requiredNonNegativeNumber(record: Record<string, unknown>, key: string): number {
  const value = record[key];
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    throw new Error(`${key} 必须是非负数`);
  }
  return value;
}

async function responseError(response: Response) {
  const text = await response.text();
  try {
    const body = asRecord(JSON.parse(text) as unknown, "error response");
    return typeof body.error === "string" ? body.error : `HTTP ${response.status}`;
  } catch {
    return `HTTP ${response.status}`;
  }
}
