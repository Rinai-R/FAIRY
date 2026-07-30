// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { UsagePage } from "./MorePages";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

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

function setup() {
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
  vi.stubGlobal("localStorage", {
    getItem: () => "",
    setItem: () => undefined,
    removeItem: () => undefined,
    clear: () => undefined,
    key: () => null,
    length: 0,
  });
}

describe("UsagePage", () => {
  it("shows aggregate metrics, truncation, and synchronized respond filtering", async () => {
    setup();
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(usageReport()), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    render(<Theme><UsagePage onToast={() => undefined} /></Theme>);
    expect((await screen.findByTestId("usage-cached")).textContent).toContain("400");
    expect(screen.getByTestId("usage-uncached").textContent).toContain("1,100");
    expect(screen.getByTestId("usage-output").textContent).toContain("150");
    expect(screen.getByTestId("usage-hit-rate").textContent).toContain("40.0%");
    expect(screen.getByText("仅展示最近记录，累计仍覆盖全部历史")).toBeTruthy();
    expect(screen.getByTestId("usage-turn-turn-respond")).toBeTruthy();
    expect(screen.getByTestId("usage-turn-turn-compact")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "仅 respond" }));
    expect(screen.getByTestId("usage-uncached").textContent).toContain("600");
    expect(screen.getByTestId("usage-output").textContent).toContain("100");
    expect(screen.getByTestId("usage-turn-turn-respond")).toBeTruthy();
    expect(screen.queryByTestId("usage-turn-turn-compact")).toBeNull();
  });

  it("distinguishes missing cache observations and a valid empty report", async () => {
    setup();
    const noObservation = usageReport({
      overall: [lane({ cachedInputTokens: 0, cachedObservedInputTokens: 0 })],
      turns: [],
      turnCount: 0,
      truncated: false,
    });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(noObservation), { status: 200 })));
    const view = render(<Theme><UsagePage onToast={() => undefined} /></Theme>);
    expect((await screen.findByTestId("usage-hit-rate")).textContent).toContain("N/A");
    view.unmount();

    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ overall: [], turns: [], turnCount: 0, truncated: false }), { status: 200 })));
    render(<Theme><UsagePage onToast={() => undefined} /></Theme>);
    expect(await screen.findByText("还没有可统计的模型用量。")).toBeTruthy();
    expect(screen.getByText("当前筛选下没有发送记录。")).toBeTruthy();
    expect(screen.queryByTestId("usage-hit-rate")).toBeNull();
  });

  it("removes a stale snapshot on refresh failure and can retry", async () => {
    setup();
    let request = 0;
    vi.stubGlobal("fetch", vi.fn(async () => {
      request += 1;
      if (request === 2) return new Response(JSON.stringify({ error: "usage unavailable" }), { status: 503 });
      return new Response(JSON.stringify(usageReport({ truncated: false })), { status: 200 });
    }));

    render(<Theme><UsagePage onToast={() => undefined} /></Theme>);
    expect(await screen.findByTestId("usage-cached")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    expect((await screen.findByRole("alert")).textContent).toContain("usage unavailable");
    expect(screen.queryByTestId("usage-cached")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    expect(await screen.findByTestId("usage-cached")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("rejects malformed reports without rendering fabricated statistics", async () => {
    setup();
    const onToast = vi.fn();
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(usageReport({ turnCount: -1 })), { status: 200 })));
    render(<Theme><UsagePage onToast={onToast} /></Theme>);
    expect((await screen.findByRole("alert")).textContent).toContain("turnCount");
    expect(screen.queryByTestId("usage-cached")).toBeNull();
    await waitFor(() => expect(onToast).toHaveBeenCalledWith(expect.stringMatching(/turnCount/), true));
  });
});
