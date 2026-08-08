import { describe, expect, it } from "vitest";
import {
  projectRuntimeTimeline,
  runtimeEventSummary,
  runtimeModelUsageTotals,
  runtimeToolDetail,
  type RuntimeEvent,
} from "./runtimeTimeline";

function event(sequence: number, eventType: string, metadata: Record<string, unknown> = {}, state = "planning"): RuntimeEvent {
  return { sequence, eventType, state, metadata, createdAtUnixMs: 1_000 + sequence };
}

function completedRuntime(): RuntimeEvent[] {
  return [
    event(1, "transition", {}, "interpreting"),
    event(2, "transition", {}, "gathering"),
    event(3, "gather", {}, "gathering"),
    event(4, "transition", {}, "planning"),
    event(5, "prompt", { retrievedPersonalCount: 0, retrievedKnowledgeCount: 0 }),
    event(6, "continuation", { incremental: false }),
    event(7, "model", { completedMs: 2097 }),
    event(8, "model", {
      usage: [{
        lane: "respond",
        usage: {
          inputTokens: 1785,
          outputTokens: 98,
          cachedInputTokens: { status: "observed", tokens: 1600 },
        },
      }],
    }),
    event(9, "compile", { status: "completed", chainCount: 1 }),
    event(10, "transition", {}, "responding"),
    event(11, "beat_delivery", { status: "planned", kind: "utterance", chainIndex: 0, playIndex: 0 }, "responding"),
    event(12, "beat_delivery", { status: "published", kind: "utterance", chainIndex: 0, playIndex: 0 }, "responding"),
    event(13, "terminal", { status: "completed" }, "completed"),
    event(14, "context_window", { windowNumber: 1, observedPrefillTokens: 1785 }, "completed"),
  ];
}

describe("runtime timeline projection", () => {
  it("coalesces one model call and one published delivery in the key timeline", () => {
    const projected = projectRuntimeTimeline(completedRuntime(), "summary");

    expect(projected).toHaveLength(11);
    expect(projected.filter((item) => item.eventType === "model")).toHaveLength(1);
    expect(projected.filter((item) => item.eventType === "beat_delivery")).toHaveLength(1);
    expect(projected.some((item) => item.eventType === "context_window")).toBe(false);

    const model = projected.find((item) => item.eventType === "model");
    const delivery = projected.find((item) => item.eventType === "beat_delivery");
    expect(model?.sourceCount).toBe(2);
    expect(runtimeEventSummary(model!)).toBe("完成 2,097 ms，输入 1,785 Token，输出 98 Token，缓存命中 1,600 Token");
    expect(delivery?.sourceCount).toBe(2);
    expect(runtimeEventSummary(delivery!)).toBe("第 1 段已发布");
  });

  it("preserves every safe event and its order in raw mode", () => {
    const projected = projectRuntimeTimeline(completedRuntime(), "raw");

    expect(projected).toHaveLength(14);
    expect(projected.map((item) => item.sequence)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14]);
    expect(projected.filter((item) => item.eventType === "model")).toHaveLength(2);
    expect(projected.filter((item) => item.eventType === "beat_delivery")).toHaveLength(2);
    expect(runtimeEventSummary(projected[10])).toBe("第 1 段等待发布");
    expect(runtimeEventSummary(projected[13])).toBe("窗口 1，观测输入 1,785 Token");
  });

  it("keeps planned-only and failed deliveries visible without reporting success", () => {
    const planned = event(1, "beat_delivery", { status: "planned", kind: "utterance", chainIndex: 0, playIndex: 0 }, "responding");
    const failed = event(2, "beat_delivery", { status: "failed", kind: "utterance", chainIndex: 1, playIndex: 0 }, "failed");
    const projected = projectRuntimeTimeline([planned, failed], "summary");

    expect(projected).toHaveLength(2);
    expect(runtimeEventSummary(projected[0])).toBe("第 1 段等待发布");
    expect(runtimeEventSummary(projected[1])).toBe("第 2 段发布失败");
  });

  it("does not merge delivery events when their identity is incomplete", () => {
    const projected = projectRuntimeTimeline([
      event(1, "beat_delivery", { status: "planned" }, "responding"),
      event(2, "beat_delivery", { status: "published" }, "responding"),
    ], "summary");

    expect(projected).toHaveLength(2);
  });

  it("aggregates observed model usage across calls without inventing missing values", () => {
    const totals = runtimeModelUsageTotals([
      event(1, "model", { completedMs: 1200 }),
      event(2, "model", {
        usage: [{
          lane: "gather",
          usage: {
            inputTokens: 400,
            cachedInputTokens: { status: "observed", tokens: 300 },
          },
        }],
      }),
      event(3, "tool", { usage: [{ usage: { inputTokens: 999 } }] }),
      event(4, "model", {
        usage: [{
          lane: "respond",
          usage: {
            inputTokens: 600,
            outputTokens: 80,
            cachedInputTokens: { status: "missing" },
          },
        }],
      }),
    ]);

    expect(totals).toEqual({ input: 1000, output: 80, cached: 300 });
    expect(runtimeModelUsageTotals([event(5, "model", { completedMs: 500 })])).toEqual({
      input: null,
      output: null,
      cached: null,
    });
  });

  it("projects versioned tool detail without trusting unknown fields", () => {
    const tool = event(1, "tool", {
      tool: "web_search",
      status: "ok",
      detail: {
        version: "v1",
        arguments: { query: "苍之彼方的四重奏", raw: "hidden" },
        receipt: { status: "ok", knowledgeCount: 1 },
        result: {
          knowledge: [{
            id: "web-1",
            topic: "web_search",
            statement: "标题 — 摘要",
            sources: [{ title: "标题", url: "https://example.com/one", snippet: "摘要", rank: 1 }],
          }],
        },
        mergedContext: {
          personalMemories: [{ id: "memory-1", kind: "preference", content: "喜欢蓝色" }],
          knowledge: [{ id: "web-1", topic: "web_search", statement: "标题 — 摘要", sources: [] }],
          socialMemories: { entries: [] },
          semanticStatus: "unavailable",
        },
      },
    });
    const detail = runtimeToolDetail(tool);

    expect(detail?.query).toBe("苍之彼方的四重奏");
    expect(detail?.result?.knowledge[0].sources[0].url).toBe("https://example.com/one");
    expect(detail?.mergedContext?.personalMemories[0].content).toBe("喜欢蓝色");
    expect(runtimeToolDetail(event(2, "tool", { detail: { version: "v2" } }))).toBeNull();
  });
});
