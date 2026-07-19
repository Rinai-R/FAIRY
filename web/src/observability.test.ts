import { describe, expect, it } from "vitest";
import {
  MAX_VISIBLE_LOGS,
  SSEParser,
  appendPendingLogs,
  mergeVisibleLogs,
  parseLogEntry,
  parseMetrics,
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
    const metrics = parseMetrics(validMetrics());
    expect(metrics.process.goVersion).toBe("go1.26");
    expect(() => parseMetrics({ ...validMetrics(), logs: {} })).toThrow();
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
    http: { inFlight: 0, total: 1, status2xx: 1, status4xx: 0, status5xx: 0, routes: [] },
    logs: { retainedEntries: 0, droppedEntries: 0, activeSubscribers: 0, slowSubscriberDisconnects: 0 },
    runtime: { activeBackgroundJobs: 0, eventSubscribers: 0 },
    usage: { overall: [], turns: [], turnCount: 0, truncated: false },
  };
}
