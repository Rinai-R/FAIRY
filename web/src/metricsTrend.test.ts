import { describe, expect, it } from "vitest";
import type { MetricsSnapshot } from "./observability";
import {
  appendMetricTrend,
  buildSegmentedLinePaths,
  buildLineGeometry,
  chartDomainMax,
  nearestMetricTrendIndex,
  projectMetricsTrend,
  sameCoreProcess,
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
      learningEnqueued: 7,
      learningSucceeded: 5,
      feedbackRegistered: 9,
      feedbackSuperseded: 2,
      feedbackSucceeded: 6,
      feedbackModelCalls: 5,
      feedbackInputTokens: 1_200,
      feedbackCachedObservedInputTokens: 1_000,
      feedbackCachedInputTokens: 700,
      feedbackCacheWriteTokens: 100,
      feedbackOutputTokens: 80,
      compactionL1Applied: 4,
      compactionL2Applied: 2,
      compactionL3Applied: 1,
      compactionFailed: 3,
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

  it("maps the plot pointer to the nearest bounded sample", () => {
    expect(nearestMetricTrendIndex(48, 0, 640, 5)).toBe(0);
    expect(nearestMetricTrendIndex(338, 0, 640, 5)).toBe(2);
    expect(nearestMetricTrendIndex(628, 0, 640, 5)).toBe(4);
    expect(nearestMetricTrendIndex(-200, 0, 640, 5)).toBe(0);
    expect(nearestMetricTrendIndex(900, 0, 640, 5)).toBe(4);
    expect(nearestMetricTrendIndex(10, 0, 0, 5)).toBe(-1);
  });

  it("breaks lines at Core process boundaries without losing hover points", () => {
    const geometry = buildLineGeometry([1, 2, 3, 4], 4, 200, 100);
    expect(buildSegmentedLinePaths(geometry.points, [1_000, 1_000, 8_000, 8_000])).toEqual([
      "M48 60 L94.67 44",
      "M141.33 28 L188 12",
    ]);
    expect(sameCoreProcess(1_000, 2_900)).toBe(true);
    expect(sameCoreProcess(1_000, 3_100)).toBe(false);
    expect(buildSegmentedLinePaths(geometry.points, [1])).toEqual([]);
  });
});

function trendPoint(timestampUnixMs: number, httpTotal: number): MetricsTrendPoint {
  return {
    timestampUnixMs,
    processStartedAtUnixMs: 1,
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
    learningEnqueued: 0,
    learningSucceeded: 0,
    learningFailed: 0,
    learningDropped: 0,
    feedbackRegistered: 0,
    feedbackSuperseded: 0,
    feedbackSucceeded: 0,
    feedbackFailed: 0,
    feedbackDropped: 0,
    feedbackModelCalls: 0,
    feedbackInputTokens: 0,
    feedbackCachedObservedInputTokens: 0,
    feedbackCachedInputTokens: 0,
    feedbackCacheWriteTokens: 0,
    feedbackOutputTokens: 0,
    compactionL1Applied: 0,
    compactionL2Applied: 0,
    compactionL3Applied: 0,
    compactionFailed: 0,
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
    runtime: {
      activeBackgroundJobs: 1,
      eventSubscribers: 4,
      compaction: { l1Applied: 4, l2Applied: 2, l3Applied: 1, failed: 3 },
      experience: {
        learning: { enqueued: 7, dropped: 1, succeeded: 5, failed: 1 },
        feedback: {
          registered: 9, superseded: 2, dropped: 1, succeeded: 6, failed: 2,
          modelCalls: 5, inputTokens: 1_200, cachedObservedInputTokens: 1_000,
          cachedInputTokens: 700, cacheWriteTokens: 100, outputTokens: 80,
        },
        cacheIdentityVersion: "prompt-cache-v2",
      },
    },
    usage: {
      overall: [
        { lane: "respond", inputTokens: 100, outputTokens: 20, cachedInputTokens: 40, cachedObservedInputTokens: 100, cacheWriteTokens: 0, callCount: 2 },
        { lane: "tool", inputTokens: 30, outputTokens: 5, cachedInputTokens: 10, cachedObservedInputTokens: 30, cacheWriteTokens: 0, callCount: 1 },
      ],
      turns: [], turnCount: 2, truncated: false,
    },
    history: [],
  };
}
