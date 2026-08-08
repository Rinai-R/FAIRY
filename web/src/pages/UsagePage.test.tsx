// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it } from "vitest";
import { UsageDashboard } from "./ObservabilityPage";

afterEach(() => cleanup());

function lane(overrides: Record<string, unknown> = {}) {
  return {
    lane: "respond",
    inputTokens: 1_000,
    outputTokens: 100,
    cachedInputTokens: 400,
    cachedObservedInputTokens: 1_000,
    cacheWriteTokens: 50,
    callCount: 2,
    ...overrides,
  };
}

function usageReport(overrides: Record<string, unknown> = {}) {
  const respond = lane();
  const compact = lane({ lane: "compact", inputTokens: 500, outputTokens: 50, cachedInputTokens: 0, cachedObservedInputTokens: 0, cacheWriteTokens: 0, callCount: 1 });
  return {
    overall: [respond, compact],
    turns: [
      { conversationId: "c1", turnId: "turn-respond", characterId: "character-one", createdAtUnixMs: 1_700_000_000_000, status: "completed", lanes: [respond, compact] },
      { conversationId: "c2", turnId: "turn-compact", characterId: "character-two", createdAtUnixMs: 1_699_000_000_000, status: "failed", lanes: [compact] },
    ],
    turnCount: 2,
    truncated: true,
    ...overrides,
  };
}

describe("UsageDashboard", () => {
  it("projects the shared snapshot and keeps aggregate and turn filtering synchronized", () => {
    render(<Theme><UsageDashboard report={usageReport()} /></Theme>);

    expect(screen.getByTestId("usage-cached").textContent).toContain("400");
    expect(screen.getByTestId("usage-uncached").textContent).toContain("1,100");
    expect(screen.getByTestId("usage-output").textContent).toContain("150");
    expect(screen.getByTestId("usage-hit-rate").textContent).toContain("40.0%");
    expect(screen.getByText("仅展示最近记录，累计仍覆盖全部历史")).toBeTruthy();
    expect(screen.getByTestId("usage-turn-turn-respond")).toBeTruthy();
    expect(screen.getByTestId("usage-turn-turn-compact")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "仅回复" }));
    expect(screen.getByTestId("usage-uncached").textContent).toContain("600");
    expect(screen.getByTestId("usage-output").textContent).toContain("100");
    expect(screen.getByTestId("usage-turn-turn-respond")).toBeTruthy();
    expect(screen.queryByTestId("usage-turn-turn-compact")).toBeNull();
    expect(screen.getByText("已完成")).toBeTruthy();
  });

  it("distinguishes missing cache observations from a valid empty report", () => {
    const noObservation = usageReport({
      overall: [lane({ cachedInputTokens: 0, cachedObservedInputTokens: 0 })],
      turns: [],
      turnCount: 0,
      truncated: false,
    });
    const view = render(<Theme><UsageDashboard report={noObservation} /></Theme>);
    expect(screen.getByTestId("usage-hit-rate").textContent).toContain("N/A");

    view.rerender(<Theme><UsageDashboard report={{ overall: [], turns: [], turnCount: 0, truncated: false }} /></Theme>);
    expect(screen.getByText("还没有模型用量")).toBeTruthy();
    expect(screen.queryByTestId("usage-hit-rate")).toBeNull();
  });
});
