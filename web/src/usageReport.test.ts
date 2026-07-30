import { describe, expect, it } from "vitest";
import {
  USAGE_LANE_FILTER_ALL,
  USAGE_LANE_FILTER_RESPOND,
  aggregateUsage,
  formatHitRate,
  formatTokenCount,
  parseUsageReport,
  turnMatchesLane,
  usageHitRate,
  type UsageLane,
} from "./usageReport";

function lane(overrides: Partial<UsageLane> = {}): UsageLane {
  return {
    lane: "respond",
    inputTokens: 1_000,
    outputTokens: 100,
    cachedInputTokens: 400,
    cachedObservedInputTokens: 1_000,
    cacheWriteTokens: 0,
    callCount: 1,
    ...overrides,
  };
}

function report() {
  return {
    overall: [lane()],
    turns: [{
      conversationId: "conversation-1",
      turnId: "turn-1",
      characterId: "character-1",
      createdAtUnixMs: 1_700_000_000_000,
      status: "completed",
      lanes: [lane()],
    }],
    turnCount: 1,
    truncated: false,
  };
}

describe("usage report parsing", () => {
  it("accepts the complete report shape", () => {
    expect(parseUsageReport(report())).toEqual(report());
  });

  it("rejects missing, unexpected, and invalid values", () => {
    const missing = report() as Record<string, unknown>;
    delete missing.turnCount;
    expect(() => parseUsageReport(missing)).toThrow(/turnCount/);
    expect(() => parseUsageReport({ ...report(), privateDecision: "hidden" })).toThrow(/未知字段/);
    expect(() => parseUsageReport({ ...report(), overall: [lane({ inputTokens: -1 })] })).toThrow(/inputTokens/);
    expect(() => parseUsageReport({ ...report(), turns: null })).toThrow(/turns/);
  });

  it("accepts a valid empty and truncated report", () => {
    expect(parseUsageReport({ overall: [], turns: [], turnCount: 20, truncated: true })).toEqual({
      overall: [],
      turns: [],
      turnCount: 20,
      truncated: true,
    });
  });
});

describe("usage projections", () => {
  it("aggregates all lanes and derives non-negative uncached input", () => {
    const total = aggregateUsage([
      lane(),
      lane({ lane: "compact", inputTokens: 500, cachedInputTokens: 700, cachedObservedInputTokens: 700 }),
    ], USAGE_LANE_FILTER_ALL);
    expect(total.inputTokens).toBe(1_500);
    expect(total.cachedInputTokens).toBe(1_100);
    expect(total.outputTokens).toBe(200);
    expect(total.uncachedInputTokens).toBe(400);
  });

  it("filters aggregates and turns to respond", () => {
    const lanes = [lane(), lane({ lane: "compact", inputTokens: 500 })];
    expect(aggregateUsage(lanes, USAGE_LANE_FILTER_RESPOND).inputTokens).toBe(1_000);
    expect(turnMatchesLane({ ...report().turns[0], lanes }, USAGE_LANE_FILTER_RESPOND)).toBe(true);
    expect(turnMatchesLane({ ...report().turns[0], lanes: [lane({ lane: "compact" })] }, USAGE_LANE_FILTER_RESPOND)).toBe(false);
  });

  it("uses observed input for hit rate and preserves missing observations", () => {
    expect(formatHitRate(usageHitRate(aggregateUsage([lane()], USAGE_LANE_FILTER_ALL)))).toBe("40.0%");
    const missing = aggregateUsage([
      lane({ cachedInputTokens: 0, cachedObservedInputTokens: 0 }),
    ], USAGE_LANE_FILTER_ALL);
    expect(usageHitRate(missing)).toBeNull();
    expect(formatHitRate(usageHitRate(missing))).toBe("N/A");
  });

  it("formats token counts for scanning", () => {
    expect(formatTokenCount(1_234_567)).toBe("1,234,567");
  });
});

