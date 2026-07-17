// Strict parsing and view aggregation for the token usage report surfaced by
// MemoryService.TokenUsageReport(). The backend is the source of truth; this
// module refuses malformed payloads instead of guessing zeros.

export const USAGE_LANE_FILTER_ALL = "all";

const USAGE_REPORT_KEYS = new Set(["overall", "turns", "turnCount", "truncated"]);
const USAGE_LANE_KEYS = new Set([
  "lane",
  "inputTokens",
  "outputTokens",
  "cachedInputTokens",
  "cachedObservedInputTokens",
  "cacheWriteTokens",
  "callCount",
]);
const USAGE_TURN_KEYS = new Set([
  "conversationId",
  "turnId",
  "characterId",
  "createdAtUnixMs",
  "status",
  "lanes",
]);

function requireObject(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(label + " must be an object");
  }
  return value;
}

function rejectUnexpectedKeys(value, allowed, label) {
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) {
      throw new TypeError(label + " contains unexpected field " + key);
    }
  }
}

function requireArray(value, label) {
  if (!Array.isArray(value)) {
    throw new TypeError(label + " must be an array");
  }
  return value;
}

function requireString(value, label) {
  if (typeof value !== "string" || value.length === 0) {
    throw new TypeError(label + " must be a non-empty string");
  }
  return value;
}

function requireOptionalString(value, label) {
  if (typeof value !== "string") {
    throw new TypeError(label + " must be a string");
  }
  return value;
}

function requireNonNegativeInteger(value, label) {
  if (!Number.isSafeInteger(value) || value < 0) {
    throw new TypeError(label + " must be a non-negative integer");
  }
  return value;
}

function requireBoolean(value, label) {
  if (typeof value !== "boolean") {
    throw new TypeError(label + " must be a boolean");
  }
  return value;
}

function parseLaneAggregate(value, label) {
  requireObject(value, label);
  rejectUnexpectedKeys(value, USAGE_LANE_KEYS, label);
  return Object.freeze({
    lane: requireString(value.lane, label + ".lane"),
    inputTokens: requireNonNegativeInteger(value.inputTokens, label + ".inputTokens"),
    outputTokens: requireNonNegativeInteger(value.outputTokens, label + ".outputTokens"),
    cachedInputTokens: requireNonNegativeInteger(value.cachedInputTokens, label + ".cachedInputTokens"),
    cachedObservedInputTokens: requireNonNegativeInteger(
      value.cachedObservedInputTokens,
      label + ".cachedObservedInputTokens",
    ),
    cacheWriteTokens: requireNonNegativeInteger(value.cacheWriteTokens, label + ".cacheWriteTokens"),
    callCount: requireNonNegativeInteger(value.callCount, label + ".callCount"),
  });
}

function parseUsageTurn(value, label) {
  requireObject(value, label);
  rejectUnexpectedKeys(value, USAGE_TURN_KEYS, label);
  const lanes = requireArray(value.lanes, label + ".lanes").map((lane, index) =>
    parseLaneAggregate(lane, label + ".lanes[" + index + "]"),
  );
  return Object.freeze({
    conversationId: requireString(value.conversationId, label + ".conversationId"),
    turnId: requireString(value.turnId, label + ".turnId"),
    characterId: requireOptionalString(value.characterId, label + ".characterId"),
    createdAtUnixMs: requireNonNegativeInteger(value.createdAtUnixMs, label + ".createdAtUnixMs"),
    status: requireString(value.status, label + ".status"),
    lanes: Object.freeze(lanes),
  });
}

export function parseUsageReport(value) {
  requireObject(value, "usage report");
  rejectUnexpectedKeys(value, USAGE_REPORT_KEYS, "usage report");
  const overall = requireArray(value.overall, "usage report.overall").map((lane, index) =>
    parseLaneAggregate(lane, "usage report.overall[" + index + "]"),
  );
  const turns = requireArray(value.turns, "usage report.turns").map((turn, index) =>
    parseUsageTurn(turn, "usage report.turns[" + index + "]"),
  );
  return Object.freeze({
    overall: Object.freeze(overall),
    turns: Object.freeze(turns),
    turnCount: requireNonNegativeInteger(value.turnCount, "usage report.turnCount"),
    truncated: requireBoolean(value.truncated, "usage report.truncated"),
  });
}

function emptyAggregate() {
  return {
    inputTokens: 0,
    outputTokens: 0,
    cachedInputTokens: 0,
    cachedObservedInputTokens: 0,
    cacheWriteTokens: 0,
    callCount: 0,
    uncachedInputTokens: 0,
  };
}

// aggregateLanes collapses a lane list into one total for the selected filter.
// laneFilter === USAGE_LANE_FILTER_ALL sums every lane; otherwise only the
// matching lane contributes.
export function aggregateLanes(lanes, laneFilter = USAGE_LANE_FILTER_ALL) {
  const total = emptyAggregate();
  for (const lane of lanes) {
    if (laneFilter !== USAGE_LANE_FILTER_ALL && lane.lane !== laneFilter) continue;
    total.inputTokens += lane.inputTokens;
    total.outputTokens += lane.outputTokens;
    total.cachedInputTokens += lane.cachedInputTokens;
    total.cachedObservedInputTokens += lane.cachedObservedInputTokens;
    total.cacheWriteTokens += lane.cacheWriteTokens;
    total.callCount += lane.callCount;
  }
  total.uncachedInputTokens = Math.max(0, total.inputTokens - total.cachedInputTokens);
  return Object.freeze(total);
}

export function overallUsageForLane(report, laneFilter = USAGE_LANE_FILTER_ALL) {
  return aggregateLanes(report.overall, laneFilter);
}

export function turnUsageForLane(turn, laneFilter = USAGE_LANE_FILTER_ALL) {
  return aggregateLanes(turn.lanes, laneFilter);
}

// usageHitRate returns cached / observed-input for the aggregate, or null when
// no cache observation exists (so the UI shows N/A rather than a fake 0%).
export function usageHitRate(aggregate) {
  if (!aggregate || aggregate.cachedObservedInputTokens <= 0) return null;
  return aggregate.cachedInputTokens / aggregate.cachedObservedInputTokens;
}

export function availableLaneFilters(report) {
  const lanes = new Set();
  for (const lane of report.overall) lanes.add(lane.lane);
  for (const turn of report.turns) {
    for (const lane of turn.lanes) lanes.add(lane.lane);
  }
  return [USAGE_LANE_FILTER_ALL, ...Array.from(lanes).sort()];
}

const TOKEN_FORMATTER = new Intl.NumberFormat("en-US");

export function formatTokenCount(value) {
  return TOKEN_FORMATTER.format(value);
}

export function formatHitRate(rate) {
  if (rate === null || rate === undefined) return "N/A";
  return (rate * 100).toFixed(1) + "%";
}
