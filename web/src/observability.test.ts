import { describe, expect, it } from "vitest";
import {
  MAX_VISIBLE_LOGS,
  SSEParser,
  appendPendingLogs,
  mergeVisibleLogs,
  parseLogEntry,
  parseMetrics,
  parseTraceDetail,
  type LogEntry,
} from "./observability";

function log(sequence: number): LogEntry {
  return {
    sequence,
    timestampUnixMs: sequence,
    level: "info",
    logger: "test",
    message: `log-${sequence}`,
    messageTruncated: false,
    fields: [],
    fieldsTruncated: false,
  };
}

describe("observability parsers", () => {
  it("rejects incomplete and unknown log entries", () => {
    expect(() => parseLogEntry({ level: "info" })).toThrow();
    expect(() => parseLogEntry({ ...log(1), level: "verbose" })).toThrow(/未知日志级别/);
  });

  it("parses complete metrics and rejects missing fields", () => {
    const input = validMetrics();
    input.http.routes = [{
      method: "GET", route: "/v1/session/ws", longLived: true,
      requestCount: 1, errorCount: 0, totalDurationMs: 0, maxDurationMs: 0,
    }];
    const metrics = parseMetrics(input);
    expect(metrics.messagesAvailable).toBe(true);
    expect(metrics.process.goVersion).toBe("go1.26");
    expect(metrics.messages.received).toBe(3);
    expect(metrics.messages.latencies.receiveToCompleted.maxDurationMs).toBe(65);
    expect(metrics.http.routes[0]?.longLived).toBe(true);
    expect(() => parseMetrics({ ...validMetrics(), logs: {} })).toThrow();
    expect(() => parseMetrics({ ...validMetrics(), messages: {} })).toThrow();
  });

  it("projects usage turns through a safe whitelist", () => {
    const input = validMetrics();
    input.usage.turns = [{
      conversationId: "conversation-1",
      turnId: "turn-1",
      characterId: "character-1",
      createdAtUnixMs: 1,
      status: "completed",
      lanes: [],
      decision: "private internal decision",
    }];
    const parsed = parseMetrics(input);
    expect(parsed.usage.turns).toEqual([{
      conversationId: "conversation-1",
      turnId: "turn-1",
      characterId: "character-1",
      createdAtUnixMs: 1,
      status: "completed",
      lanes: [],
    }]);
    expect(JSON.stringify(parsed)).not.toContain("private internal decision");
  });

  it("parses low-sensitivity experience counters without projecting unknown fields", () => {
    const input = validMetrics();
    input.runtime.experience = {
      learning: { enqueued: 4, dropped: 1, succeeded: 2, failed: 1 },
      feedback: { registered: 3, dropped: 1, succeeded: 1, failed: 1 },
      cacheIdentityVersion: "prompt-cache-v2",
      promptCacheKey: "secret-provider-key",
      evidence: "private observation",
    };
    const parsed = parseMetrics(input);
    expect(parsed.runtime.experience).toEqual({
      learning: { enqueued: 4, dropped: 1, succeeded: 2, failed: 1 },
      feedback: { registered: 3, dropped: 1, succeeded: 1, failed: 1 },
      cacheIdentityVersion: "prompt-cache-v2",
    });
    expect(JSON.stringify(parsed.runtime.experience)).not.toContain("secret-provider-key");
    expect(JSON.stringify(parsed.runtime.experience)).not.toContain("private observation");
  });

  it("accepts legacy metrics without message telemetry", () => {
    const { messages: _messages, ...legacyMetrics } = validMetrics();
    const metrics = parseMetrics(legacyMetrics);

    expect(metrics.messagesAvailable).toBe(false);
    expect(metrics.messages.received).toBe(0);
    expect(metrics.messages.latencies.receiveToCompleted.observations).toBe(0);
    expect(metrics.messages.recent).toEqual([]);
  });

  it("parses a complete trace tree and rejects malformed hierarchy or timing", () => {
    const trace = parseTraceDetail(validTraceDetail());
    expect(trace.traceId).toBe("msg-3");
    expect(trace.spans.map((span) => span.operation)).toEqual(["消息处理", "Turn", "模型调用"]);
    expect(trace.spans[2]?.attributes).toEqual({ lane: "respond", model: "deepseek-v4-flash" });

    const missingParent = validTraceDetail();
    missingParent.spans[1].parentSpanId = "unknown";
    expect(() => parseTraceDetail(missingParent)).toThrow(/parentSpanId 不存在/);

    const cyclic = validTraceDetail();
    cyclic.spans[0].parentSpanId = "span-model";
    expect(() => parseTraceDetail(cyclic)).toThrow(/父子环/);

    const wrongDuration = validTraceDetail();
    wrongDuration.spans[2].durationMs = 49;
    expect(() => parseTraceDetail(wrongDuration)).toThrow(/起止时间与耗时不一致/);
  });

  it("parses split SSE frames and rejects incomplete final data", () => {
    const parser = new SSEParser();
    expect(parser.push("event: rea")).toEqual([]);
    expect(parser.push("dy\ndata: {\"ok\":true}\n\n")[0]?.event).toBe("ready");
    parser.push("event: log\ndata: {}");
    expect(() => parser.finish()).toThrow(/不完整/);
  });
});

describe("bounded sequence merge", () => {
  it("deduplicates, sorts, and retains the newest 500 entries", () => {
    const input = Array.from({ length: MAX_VISIBLE_LOGS + 20 }, (_, index) => log(index + 1));
    const result = mergeVisibleLogs([log(2)], input);
    expect(result.entries).toHaveLength(MAX_VISIBLE_LOGS);
    expect(result.entries[0]?.sequence).toBe(21);
    expect(result.entries.at(-1)?.sequence).toBe(520);
    expect(result.dropped).toBe(20);
  });

  it("bounds paused pending entries with the same policy", () => {
    const result = appendPendingLogs([], Array.from({ length: 503 }, (_, index) => log(index + 1)));
    expect(result.entries[0]?.sequence).toBe(4);
    expect(result.dropped).toBe(3);
  });
});

export function validMetrics() {
  return {
    generatedAtUnixMs: 1,
    process: { uptimeSeconds: 1, goVersion: "go1.26", goroutines: 2, heapAllocBytes: 3 },
    http: { inFlight: 0, total: 1, status2xx: 1, status4xx: 0, status5xx: 0, routes: [] as Array<Record<string, unknown>> },
    logs: { retainedEntries: 0, droppedEntries: 0, activeSubscribers: 0, slowSubscriberDisconnects: 0 },
    messages: validMessageMetrics(),
    runtime: {
      activeBackgroundJobs: 0,
      eventSubscribers: 0,
      experience: undefined as undefined | Record<string, unknown>,
    },
    usage: {
      overall: [] as Array<Record<string, unknown>>,
      turns: [] as Array<Record<string, unknown>>,
      turnCount: 0,
      truncated: false,
    },
  };
}

export function validTraceDetail() {
  return {
    traceId: "msg-3",
    conversationId: "conversation-1",
    turnId: "turn-1",
    source: "ambient",
    status: "completed",
    startedAtUnixMs: 1,
    endedAtUnixMs: 66,
    durationMs: 65,
    droppedSpanCount: 0,
    truncated: false,
    spans: [
      {
        spanId: "span-root",
        parentSpanId: "",
        operation: "消息处理",
        category: "message",
        status: "completed",
        startedAtUnixMs: 1,
        endedAtUnixMs: 66,
        durationMs: 65,
        attributes: { source: "ambient" },
      },
      {
        spanId: "span-turn",
        parentSpanId: "span-root",
        operation: "Turn",
        category: "turn",
        status: "completed",
        startedAtUnixMs: 5,
        endedAtUnixMs: 65,
        durationMs: 60,
        attributes: { turn_id: "turn-1" },
      },
      {
        spanId: "span-model",
        parentSpanId: "span-turn",
        operation: "模型调用",
        category: "model",
        status: "completed",
        startedAtUnixMs: 10,
        endedAtUnixMs: 60,
        durationMs: 50,
        attributes: { lane: "respond", model: "deepseek-v4-flash" },
      },
    ],
  };
}

function validMessageMetrics() {
  const latency = { observations: 1, totalDurationMs: 65, maxDurationMs: 65 };
  return {
    received: 3, sent: 1, directReceived: 1, ambientReceived: 2,
    completed: 1, failed: 0, interrupted: 0, silent: 1, active: 1, droppedEvents: 0,
    latencies: {
      receiveToDecision: latency, receiveToTurn: latency, turnToFirstBeat: latency,
      turnToCompleted: latency, receiveToFirstBeat: latency, receiveToCompleted: latency,
    },
    recent: [{ traceId: "msg-3", source: "ambient", conversationId: "c1", turnId: "t1", status: "completed", receivedAtUnixMs: 1, completedAtUnixMs: 66, totalDurationMs: 65 }],
  };
}
