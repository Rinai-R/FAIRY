export const USAGE_LANE_FILTER_ALL = "all" as const;
export const USAGE_LANE_FILTER_RESPOND = "respond" as const;

export type UsageLaneFilter = typeof USAGE_LANE_FILTER_ALL | typeof USAGE_LANE_FILTER_RESPOND;

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

export type UsageReport = {
  overall: UsageLane[];
  turns: UsageTurn[];
  turnCount: number;
  truncated: boolean;
};

export type UsageAggregate = Omit<UsageLane, "lane"> & {
  uncachedInputTokens: number;
};

const REPORT_KEYS = new Set(["overall", "turns", "turnCount", "truncated"]);
const LANE_KEYS = new Set([
  "lane",
  "inputTokens",
  "outputTokens",
  "cachedInputTokens",
  "cachedObservedInputTokens",
  "cacheWriteTokens",
  "callCount",
]);
const TURN_KEYS = new Set([
  "conversationId",
  "turnId",
  "characterId",
  "createdAtUnixMs",
  "status",
  "lanes",
]);

export function parseUsageReport(value: unknown): UsageReport {
  const report = requiredRecord(value, "usage report");
  rejectUnexpectedKeys(report, REPORT_KEYS, "usage report");
  return {
    overall: requiredArray(report.overall, "usage report.overall").map((lane, index) =>
      parseUsageLane(lane, `usage report.overall[${index}]`),
    ),
    turns: requiredArray(report.turns, "usage report.turns").map((turn, index) =>
      parseUsageTurn(turn, `usage report.turns[${index}]`),
    ),
    turnCount: requiredNonNegativeInteger(report, "turnCount", "usage report"),
    truncated: requiredBoolean(report, "truncated", "usage report"),
  };
}

export function aggregateUsage(lanes: UsageLane[], filter: UsageLaneFilter): UsageAggregate {
  const total: UsageAggregate = {
    inputTokens: 0,
    outputTokens: 0,
    cachedInputTokens: 0,
    cachedObservedInputTokens: 0,
    cacheWriteTokens: 0,
    callCount: 0,
    uncachedInputTokens: 0,
  };
  for (const lane of lanes) {
    if (filter !== USAGE_LANE_FILTER_ALL && lane.lane !== filter) continue;
    total.inputTokens += lane.inputTokens;
    total.outputTokens += lane.outputTokens;
    total.cachedInputTokens += lane.cachedInputTokens;
    total.cachedObservedInputTokens += lane.cachedObservedInputTokens;
    total.cacheWriteTokens += lane.cacheWriteTokens;
    total.callCount += lane.callCount;
  }
  total.uncachedInputTokens = Math.max(0, total.inputTokens - total.cachedInputTokens);
  return total;
}

export function turnMatchesLane(turn: UsageTurn, filter: UsageLaneFilter): boolean {
  return filter === USAGE_LANE_FILTER_ALL || turn.lanes.some((lane) => lane.lane === filter);
}

export function usageHitRate(aggregate: UsageAggregate): number | null {
  if (aggregate.cachedObservedInputTokens === 0) return null;
  return aggregate.cachedInputTokens / aggregate.cachedObservedInputTokens;
}

const tokenFormatter = new Intl.NumberFormat("zh-CN");
const dateFormatter = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

export function formatTokenCount(value: number): string {
  return tokenFormatter.format(value);
}

export function formatHitRate(rate: number | null): string {
  return rate === null ? "N/A" : `${(rate * 100).toFixed(1)}%`;
}

export function formatUsageTime(timestampUnixMs: number): string {
  return dateFormatter.format(new Date(timestampUnixMs));
}

function parseUsageLane(value: unknown, label: string): UsageLane {
  const lane = requiredRecord(value, label);
  rejectUnexpectedKeys(lane, LANE_KEYS, label);
  return {
    lane: requiredString(lane, "lane", label),
    inputTokens: requiredNonNegativeInteger(lane, "inputTokens", label),
    outputTokens: requiredNonNegativeInteger(lane, "outputTokens", label),
    cachedInputTokens: requiredNonNegativeInteger(lane, "cachedInputTokens", label),
    cachedObservedInputTokens: requiredNonNegativeInteger(lane, "cachedObservedInputTokens", label),
    cacheWriteTokens: requiredNonNegativeInteger(lane, "cacheWriteTokens", label),
    callCount: requiredNonNegativeInteger(lane, "callCount", label),
  };
}

function parseUsageTurn(value: unknown, label: string): UsageTurn {
  const turn = requiredRecord(value, label);
  rejectUnexpectedKeys(turn, TURN_KEYS, label);
  return {
    conversationId: requiredString(turn, "conversationId", label),
    turnId: requiredString(turn, "turnId", label),
    characterId: requiredOptionalString(turn, "characterId", label),
    createdAtUnixMs: requiredNonNegativeInteger(turn, "createdAtUnixMs", label),
    status: requiredString(turn, "status", label),
    lanes: requiredArray(turn.lanes, `${label}.lanes`).map((lane, index) =>
      parseUsageLane(lane, `${label}.lanes[${index}]`),
    ),
  };
}

function requiredRecord(value: unknown, label: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${label} 必须是对象`);
  }
  return value as Record<string, unknown>;
}

function rejectUnexpectedKeys(record: Record<string, unknown>, allowed: Set<string>, label: string) {
  for (const key of Object.keys(record)) {
    if (!allowed.has(key)) throw new Error(`${label} 包含未知字段 ${key}`);
  }
}

function requiredArray(value: unknown, label: string): unknown[] {
  if (!Array.isArray(value)) throw new Error(`${label} 必须是数组`);
  return value;
}

function requiredString(record: Record<string, unknown>, key: string, label: string): string {
  const value = record[key];
  if (typeof value !== "string" || value.length === 0) throw new Error(`${label}.${key} 必须是非空字符串`);
  return value;
}

function requiredOptionalString(record: Record<string, unknown>, key: string, label: string): string {
  const value = record[key];
  if (typeof value !== "string") throw new Error(`${label}.${key} 必须是字符串`);
  return value;
}

function requiredNonNegativeInteger(record: Record<string, unknown>, key: string, label: string): number {
  const value = record[key];
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    throw new Error(`${label}.${key} 必须是非负安全整数`);
  }
  return value;
}

function requiredBoolean(record: Record<string, unknown>, key: string, label: string): boolean {
  const value = record[key];
  if (typeof value !== "boolean") throw new Error(`${label}.${key} 必须是布尔值`);
  return value;
}

