// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ObservabilityPage } from "./ObservabilityPage";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("ObservabilityPage lifecycle", () => {
  it("uses an Authorization header, never the URL, and aborts the stream when unmounted", async () => {
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      unobserve() {}
      disconnect() {}
    });
    const token = "test-api-token";
    vi.stubGlobal("localStorage", {
      getItem: () => token,
      setItem: () => undefined,
      removeItem: () => undefined,
      clear: () => undefined,
      key: () => null,
      length: 0,
    });
    let streamSignal: AbortSignal | null = null;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n"));
      },
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.includes("/logs/stream")) {
        streamSignal = init?.signal ?? null;
        return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      }
      return new Response(JSON.stringify(validMetrics()), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const view = render(<Theme><ObservabilityPage token="" /></Theme>);
    await waitFor(() => expect(streamSignal).not.toBeNull());
    for (const [input, init] of fetchMock.mock.calls) {
      expect(String(input)).not.toContain(token);
      expect(new Headers(init?.headers).get("Authorization")).toBe(`Bearer ${token}`);
    }
    view.unmount();
    expect(streamSignal?.aborted).toBe(true);
  });

  it("removes a stale metrics snapshot when refresh fails", async () => {
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
    let metricsRequests = 0;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n"));
      },
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes("/logs/stream")) {
        return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      }
      metricsRequests += 1;
      if (metricsRequests === 1) {
        return new Response(JSON.stringify(validMetrics()), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ error: "usage metrics unavailable" }), {
        status: 500,
        headers: { "Content-Type": "application/json" },
      });
    }));

    render(<Theme><ObservabilityPage token="" /></Theme>);
    await screen.findByText("指标已更新");
    expect(screen.getByLabelText("运行指标摘要")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    await screen.findByText("指标不可用");
    expect(screen.getByText("usage metrics unavailable")).toBeTruthy();
    expect(screen.queryByLabelText("运行指标摘要")).toBeNull();
  });
});

function validMetrics() {
  return {
    generatedAtUnixMs: 1,
    process: { uptimeSeconds: 1, goVersion: "go1.26", goroutines: 2, heapAllocBytes: 3 },
    http: { inFlight: 0, total: 1, status2xx: 1, status4xx: 0, status5xx: 0, routes: [] },
    logs: { retainedEntries: 0, droppedEntries: 0, activeSubscribers: 0, slowSubscriberDisconnects: 0 },
    runtime: { activeBackgroundJobs: 0, eventSubscribers: 0 },
    usage: { overall: [], turns: [], turnCount: 0, truncated: false },
  };
}
