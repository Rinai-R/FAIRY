import { describe, expect, it } from "vitest";
import type { MetricsSnapshot } from "./observability";
import {
  appendMetricTrend,
  buildLineGeometry,
  chartDomainMax,
  projectMetricsTrend,
  type MetricsTrendPoint,
} from "./metricsTrend";

describe("metrics trend projection", () => {
  it("projects one strict metrics snapshot into comparable numeric series", () => {
    const point = projectMetricsTrend(snapshot());
    expect(point).toMatchObject({
      timestampUnixMs: 1000,
      httpTotal: 18,
      httpStatus4xx: 2,
      messagesReceived: 7,
      inputTokens: 130,
      cachedInputTokens: 50,
      outputTokens: 25,
      modelCalls: 3,
      goroutines: 11,
      heapMiB: 8,
    });
  });

  it("replaces duplicate timestamps, sorts out-of-order samples, and enforces the bound", () => {
    const first = trendPoint(1000, 1);
    const replacement = trendPoint(1000, 2);
    const history = appendMetricTrend([trendPoint(3000, 3), first, trendPoint(2000, 2)], replacement, 2);
    expect(history.map((point) => [point.timestampUnixMs, point.httpTotal])).toEqual([[2000, 2], [3000, 3]]);
    expect(() => appendMetricTrend([], first, 0)).toThrow("趋势样本上限必须是正整数");
  });

  it("creates a zero-anchored nice domain and stable geometry for one or many samples", () => {
    const points = [trendPoint(1000, 0), trendPoint(2000, 167)];
    expect(chartDomainMax(points, ["httpTotal"])).toBe(200);
    expect(chartDomainMax([trendPoint(1000, 0)], ["httpTotal"])).toBe(1);

    const single = buildLineGeometry([5], 10, 200, 100);
    expect(single.path).toBe("M118 44");
    expect(single.points).toHaveLength(1);

    const line = buildLineGeometry([0, 10], 10, 200, 100);
    expect(line.path).toBe("M48 76 L188 12");
  });
});

function trendPoint(timestampUnixMs: number, httpTotal: number): MetricsTrendPoint {
  return {
    timestampUnixMs,
    httpTotal,
    httpInFlight: 0,
    httpStatus4xx: 0,
    httpStatus5xx: 0,
    messagesReceived: 0,
    messagesSent: 0,
    messagesActive: 0,
    messagesFailed: 0,
    inputTokens: 0,
    cachedInputTokens: 0,
    outputTokens: 0,
    modelCalls: 0,
    goroutines: 0,
    backgroundJobs: 0,
    eventSubscribers: 0,
    logSubscribers: 0,
    heapMiB: 0,
  };
}

function snapshot(): MetricsSnapshot {
  const latency = { observations: 0, totalDurationMs: 0, maxDurationMs: 0 };
  return {
    generatedAtUnixMs: 1000,
    messagesAvailable: true,
    process: { uptimeSeconds: 10, goVersion: "go1.26", goroutines: 11, heapAllocBytes: 8 * 1024 * 1024 },
    http: { inFlight: 1, total: 18, status2xx: 16, status4xx: 2, status5xx: 0, routes: [] },
    logs: { retainedEntries: 5, droppedEntries: 0, activeSubscribers: 2, slowSubscriberDisconnects: 0 },
    messages: {
      received: 7, sent: 6, directReceived: 7, ambientReceived: 0, completed: 5, failed: 1,
      interrupted: 0, silent: 0, active: 1, droppedEvents: 0,
      latencies: { receiveToDecision: latency, receiveToTurn: latency, turnToFirstBeat: latency, turnToCompleted: latency, receiveToFirstBeat: latency, receiveToCompleted: latency },
      recent: [],
    },
    runtime: { activeBackgroundJobs: 1, eventSubscribers: 4 },
    usage: {
      overall: [
        { lane: "respond", inputTokens: 100, outputTokens: 20, cachedInputTokens: 40, cachedObservedInputTokens: 100, cacheWriteTokens: 0, callCount: 2 },
        { lane: "tool", inputTokens: 30, outputTokens: 5, cachedInputTokens: 10, cachedObservedInputTokens: 30, cacheWriteTokens: 0, callCount: 1 },
      ],
      turns: [], turnCount: 2, truncated: false,
    },
  };
}
