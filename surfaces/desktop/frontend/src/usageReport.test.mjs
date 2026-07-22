import assert from "node:assert/strict";
import { test } from "node:test";

import {
  USAGE_LANE_FILTER_ALL,
  aggregateLanes,
  availableLaneFilters,
  formatHitRate,
  formatTokenCount,
  overallUsageForLane,
  parseUsageReport,
  turnUsageForLane,
  usageHitRate,
} from "./usageReport.mjs";

function laneAggregate(overrides = {}) {
  return {
    lane: "respond",
    inputTokens: 1000,
    outputTokens: 100,
    cachedInputTokens: 400,
    cachedObservedInputTokens: 1000,
    cacheWriteTokens: 0,
    callCount: 1,
    ...overrides,
  };
}

function sampleReport() {
  return {
    overall: [laneAggregate({ inputTokens: 2500, outputTokens: 200, cachedInputTokens: 1000, cachedObservedInputTokens: 2500, callCount: 2 })],
    turns: [
      {
        conversationId: "conversation-1",
        turnId: "turn-1",
        characterId: "character-1",
        createdAtUnixMs: 1700000000000,
        status: "completed",
        lanes: [laneAggregate({ inputTokens: 2500, outputTokens: 200, cachedInputTokens: 1000, cachedObservedInputTokens: 2500, callCount: 2 })],
      },
    ],
    turnCount: 1,
    truncated: false,
  };
}

test("parseUsageReport accepts a well-formed report", () => {
  const report = parseUsageReport(sampleReport());
  assert.equal(report.turnCount, 1);
  assert.equal(report.turns[0].lanes[0].lane, "respond");
  assert.equal(report.overall[0].inputTokens, 2500);
});

test("parseUsageReport rejects unexpected fields", () => {
  const malformed = sampleReport();
  malformed.extra = true;
  assert.throws(() => parseUsageReport(malformed), /unexpected field extra/);
});

test("parseUsageReport rejects negative token counts", () => {
  const malformed = sampleReport();
  malformed.overall[0].inputTokens = -1;
  assert.throws(() => parseUsageReport(malformed), /inputTokens/);
});

test("parseUsageReport rejects non-array turns", () => {
  const malformed = sampleReport();
  malformed.turns = null;
  assert.throws(() => parseUsageReport(malformed), /turns must be an array/);
});

test("aggregateLanes sums all lanes and derives uncached input", () => {
  const total = aggregateLanes([
    laneAggregate({ lane: "respond", inputTokens: 1000, cachedInputTokens: 400 }),
    laneAggregate({ lane: "compact", inputTokens: 500, cachedInputTokens: 0, cachedObservedInputTokens: 0 }),
  ]);
  assert.equal(total.inputTokens, 1500);
  assert.equal(total.cachedInputTokens, 400);
  assert.equal(total.uncachedInputTokens, 1100);
});

test("aggregateLanes filters to a single lane", () => {
  const total = aggregateLanes(
    [
      laneAggregate({ lane: "respond", inputTokens: 1000 }),
      laneAggregate({ lane: "compact", inputTokens: 500 }),
    ],
    "respond",
  );
  assert.equal(total.inputTokens, 1000);
});

test("usageHitRate returns null when nothing observed", () => {
  const total = aggregateLanes([
    laneAggregate({ cachedInputTokens: 0, cachedObservedInputTokens: 0 }),
  ]);
  assert.equal(usageHitRate(total), null);
  assert.equal(formatHitRate(usageHitRate(total)), "N/A");
});

test("usageHitRate divides by observed input only", () => {
  const report = parseUsageReport(sampleReport());
  const overall = overallUsageForLane(report, USAGE_LANE_FILTER_ALL);
  assert.equal(usageHitRate(overall), 0.4);
  assert.equal(formatHitRate(0.4), "40.0%");
});

test("turnUsageForLane respects the lane filter", () => {
  const report = parseUsageReport(sampleReport());
  const usage = turnUsageForLane(report.turns[0], "respond");
  assert.equal(usage.inputTokens, 2500);
});

test("availableLaneFilters lists all plus discovered lanes", () => {
  const report = parseUsageReport({
    overall: [laneAggregate({ lane: "respond" }), laneAggregate({ lane: "compact" })],
    turns: [],
    turnCount: 0,
    truncated: false,
  });
  assert.deepEqual(availableLaneFilters(report), [USAGE_LANE_FILTER_ALL, "compact", "respond"]);
});

test("formatTokenCount groups thousands", () => {
  assert.equal(formatTokenCount(1234567), "1,234,567");
});
