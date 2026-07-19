import { getToken } from "./api";

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

export type MetricsSnapshot = {
  generatedAtUnixMs: number;
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
  runtime: { activeBackgroundJobs: number; eventSubscribers: number };
  usage: { overall: UsageLane[]; turns: unknown[]; turnCount: number; truncated: boolean };
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
  const runtime = asRecord(root.runtime, "runtime metrics");
  const usage = asRecord(root.usage, "usage metrics");
  if (!Array.isArray(http.routes)) throw new Error("metrics.http.routes 必须是数组");
  if (!Array.isArray(usage.overall) || !Array.isArray(usage.turns)) {
    throw new Error("metrics.usage 缺少数组字段");
  }
  return {
    generatedAtUnixMs: requiredPositiveInteger(root, "generatedAtUnixMs"),
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
    runtime: {
      activeBackgroundJobs: requiredNonNegativeInteger(runtime, "activeBackgroundJobs"),
      eventSubscribers: requiredNonNegativeInteger(runtime, "eventSubscribers"),
    },
    usage: {
      overall: usage.overall.map(parseUsageLane),
      turns: usage.turns,
      turnCount: requiredNonNegativeInteger(usage, "turnCount"),
      truncated: requiredBoolean(usage, "truncated"),
    },
  };
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

async function responseError(response: Response) {
  const text = await response.text();
  try {
    const body = asRecord(JSON.parse(text) as unknown, "error response");
    return typeof body.error === "string" ? body.error : `HTTP ${response.status}`;
  } catch {
    return `HTTP ${response.status}`;
  }
}
