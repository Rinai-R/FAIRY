import type { MetricsSnapshot } from "./observability";
import { aggregateUsage, USAGE_LANE_FILTER_ALL } from "./usageReport";

export const METRICS_POLL_INTERVAL_MS = 5_000;
export const MAX_METRIC_TREND_POINTS = 60;

export type MetricsTrendPoint = {
  timestampUnixMs: number;
  processStartedAtUnixMs: number;
  httpTotal: number;
  httpInFlight: number;
  httpStatus4xx: number;
  httpStatus5xx: number;
  messagesReceived: number;
  messagesSent: number;
  messagesActive: number;
  messagesFailed: number;
  inputTokens: number;
  cachedInputTokens: number;
  outputTokens: number;
  modelCalls: number;
  learningEnqueued: number;
  learningSucceeded: number;
  learningFailed: number;
  learningDropped: number;
  feedbackRegistered: number;
  feedbackSucceeded: number;
  feedbackFailed: number;
  feedbackDropped: number;
  feedbackModelCalls: number;
  feedbackInputTokens: number;
  feedbackCachedObservedInputTokens: number;
  feedbackCachedInputTokens: number;
  feedbackCacheWriteTokens: number;
  feedbackOutputTokens: number;
  compactionL1Applied: number;
  compactionL2Applied: number;
  compactionL3Applied: number;
  compactionFailed: number;
  goroutines: number;
  backgroundJobs: number;
  eventSubscribers: number;
  logSubscribers: number;
  heapMiB: number;
};

export type MetricTrendKey = Exclude<keyof MetricsTrendPoint, "timestampUnixMs" | "processStartedAtUnixMs">;

export type ChartGeometry = {
  path: string;
  points: Array<{ x: number; y: number }>;
};

export function sameCoreProcess(left: number, right: number): boolean {
  return Math.abs(left - right) <= 2_000;
}

export function buildSegmentedLinePaths(
  points: ChartGeometry["points"],
  processStartedAtUnixMs: number[],
): string[] {
  if (points.length === 0 || points.length !== processStartedAtUnixMs.length) return [];
  const paths: string[] = [];
  let current: string[] = [];
  for (let index = 0; index < points.length; index += 1) {
    const point = points[index];
    if (index === 0 || sameCoreProcess(processStartedAtUnixMs[index - 1], processStartedAtUnixMs[index])) {
      current.push(`${current.length === 0 ? "M" : "L"}${round(point.x)} ${round(point.y)}`);
      continue;
    }
    paths.push(current.join(" "));
    current = [`M${round(point.x)} ${round(point.y)}`];
  }
  if (current.length > 0) paths.push(current.join(" "));
  return paths;
}

export function projectMetricsTrend(snapshot: MetricsSnapshot): MetricsTrendPoint {
  const usage = aggregateUsage(snapshot.usage.overall, USAGE_LANE_FILTER_ALL);
  return {
    timestampUnixMs: snapshot.generatedAtUnixMs,
    processStartedAtUnixMs: Math.max(1, snapshot.generatedAtUnixMs - snapshot.process.uptimeSeconds * 1000),
    httpTotal: snapshot.http.total,
    httpInFlight: snapshot.http.inFlight,
    httpStatus4xx: snapshot.http.status4xx,
    httpStatus5xx: snapshot.http.status5xx,
    messagesReceived: snapshot.messagesAvailable ? snapshot.messages.received : 0,
    messagesSent: snapshot.messagesAvailable ? snapshot.messages.sent : 0,
    messagesActive: snapshot.messagesAvailable ? snapshot.messages.active : 0,
    messagesFailed: snapshot.messagesAvailable ? snapshot.messages.failed : 0,
    inputTokens: usage.inputTokens,
    cachedInputTokens: usage.cachedInputTokens,
    outputTokens: usage.outputTokens,
    modelCalls: usage.callCount,
    learningEnqueued: snapshot.runtime.experience.learning.enqueued,
    learningSucceeded: snapshot.runtime.experience.learning.succeeded,
    learningFailed: snapshot.runtime.experience.learning.failed,
    learningDropped: snapshot.runtime.experience.learning.dropped,
    feedbackRegistered: snapshot.runtime.experience.feedback.registered,
    feedbackSucceeded: snapshot.runtime.experience.feedback.succeeded,
    feedbackFailed: snapshot.runtime.experience.feedback.failed,
    feedbackDropped: snapshot.runtime.experience.feedback.dropped,
    feedbackModelCalls: snapshot.runtime.experience.feedback.modelCalls,
    feedbackInputTokens: snapshot.runtime.experience.feedback.inputTokens,
    feedbackCachedObservedInputTokens: snapshot.runtime.experience.feedback.cachedObservedInputTokens,
    feedbackCachedInputTokens: snapshot.runtime.experience.feedback.cachedInputTokens,
    feedbackCacheWriteTokens: snapshot.runtime.experience.feedback.cacheWriteTokens,
    feedbackOutputTokens: snapshot.runtime.experience.feedback.outputTokens,
    compactionL1Applied: snapshot.runtime.compaction.l1Applied,
    compactionL2Applied: snapshot.runtime.compaction.l2Applied,
    compactionL3Applied: snapshot.runtime.compaction.l3Applied,
    compactionFailed: snapshot.runtime.compaction.failed,
    goroutines: snapshot.process.goroutines,
    backgroundJobs: snapshot.runtime.activeBackgroundJobs,
    eventSubscribers: snapshot.runtime.eventSubscribers,
    logSubscribers: snapshot.logs.activeSubscribers,
    heapMiB: snapshot.process.heapAllocBytes / 1024 / 1024,
  };
}

export function appendMetricTrend(
  history: MetricsTrendPoint[],
  point: MetricsTrendPoint,
  limit = MAX_METRIC_TREND_POINTS,
): MetricsTrendPoint[] {
  if (!Number.isSafeInteger(limit) || limit < 1) throw new Error("趋势样本上限必须是正整数");
  const withoutSameTimestamp = history.filter((item) => item.timestampUnixMs !== point.timestampUnixMs);
  return [...withoutSameTimestamp, point]
    .sort((left, right) => left.timestampUnixMs - right.timestampUnixMs)
    .slice(-limit);
}

export function chartDomainMax(points: MetricsTrendPoint[], keys: MetricTrendKey[]): number {
  let maximum = 0;
  for (const point of points) {
    for (const key of keys) maximum = Math.max(maximum, point[key]);
  }
  if (maximum <= 0) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(maximum));
  const normalized = maximum / magnitude;
  const rounded = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10;
  return rounded * magnitude;
}

export function buildLineGeometry(
  values: number[],
  domainMax: number,
  width: number,
  height: number,
  padding = { top: 12, right: 12, bottom: 24, left: 48 },
): ChartGeometry {
  if (values.length === 0) return { path: "", points: [] };
  const plotWidth = Math.max(1, width - padding.left - padding.right);
  const plotHeight = Math.max(1, height - padding.top - padding.bottom);
  const safeMax = domainMax > 0 ? domainMax : 1;
  const points = values.map((value, index) => {
    const x = values.length === 1
      ? padding.left + plotWidth / 2
      : padding.left + (index / (values.length - 1)) * plotWidth;
    const y = padding.top + (1 - Math.min(1, Math.max(0, value / safeMax))) * plotHeight;
    return { x, y };
  });
  return {
    path: points.map((point, index) => `${index === 0 ? "M" : "L"}${round(point.x)} ${round(point.y)}`).join(" "),
    points,
  };
}

export function nearestMetricTrendIndex(
  clientX: number,
  boundsLeft: number,
  boundsWidth: number,
  pointCount: number,
  viewWidth = 640,
  padding = { left: 48, right: 12 },
): number {
  if (!Number.isFinite(clientX) || !Number.isFinite(boundsLeft) || !Number.isFinite(boundsWidth) || boundsWidth <= 0 || pointCount < 1) {
    return -1;
  }
  if (pointCount === 1) return 0;
  const plotLeft = boundsLeft + (padding.left / viewWidth) * boundsWidth;
  const plotWidth = Math.max(1, ((viewWidth - padding.left - padding.right) / viewWidth) * boundsWidth);
  const ratio = Math.min(1, Math.max(0, (clientX - plotLeft) / plotWidth));
  return Math.round(ratio * (pointCount - 1));
}

function round(value: number): number {
  return Math.round(value * 100) / 100;
}
