// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ObservabilityPage } from "./ObservabilityPage";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("ObservabilityPage lifecycle", () => {
  it("keeps logs independent from metrics, authenticates the stream, and aborts it when unmounted", async () => {
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
    const streamState: { signal: AbortSignal | null } = { signal: null };
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n"));
      },
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.includes("/logs/stream")) {
        streamState.signal = init?.signal ?? null;
        return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      }
      return new Response(JSON.stringify(validMetrics()), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const view = render(<Theme><ObservabilityPage token="" view="logs" /></Theme>);
    await waitFor(() => expect(streamState.signal).not.toBeNull());
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/logs/stream");
    expect(screen.queryByText("指标不可用")).toBeNull();
    expect(screen.queryByRole("button", { name: "刷新快照" })).toBeNull();
    for (const [input, init] of fetchMock.mock.calls) {
      expect(String(input)).not.toContain(token);
      expect(new Headers(init?.headers).get("Authorization")).toBe(`Bearer ${token}`);
    }
    view.unmount();
    expect(streamState.signal?.aborted).toBe(true);
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

    render(<Theme><ObservabilityPage token="" view="metrics" /></Theme>);
    await screen.findByText("指标已更新");
    expect(screen.getByLabelText("本次页面会话指标趋势")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "刷新快照" }));
    await screen.findByText("指标不可用");
    expect(screen.getByText("usage metrics unavailable")).toBeTruthy();
    expect(screen.queryByLabelText("本次页面会话指标趋势")).toBeNull();
  });

  it("shows message throughput, latency, and recent traces", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const stream = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n")); } });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/logs/stream")) return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      const payload = path.includes("/traces/") ? validTraceDetail() : validMetrics();
      return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    await screen.findByText("指标已更新");
    await screen.findByLabelText("端到端调用链");
    expect(await screen.findByText("模型调用")).toBeTruthy();
    expect(screen.getAllByText("msg-3").length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Span 树与时间轴")).toBeTruthy();
    expect(document.getElementById("observability-panel-metrics")?.hidden).toBe(true);
    expect(document.getElementById("observability-panel-tracing")?.hidden).toBe(false);
  });

  it("keeps legacy metrics usable and explains unavailable message telemetry", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const stream = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n")); } });
    const { messages: _messages, ...legacyMetrics } = validMetrics();
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).includes("/logs/stream")
      ? new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } })
      : new Response(JSON.stringify(legacyMetrics), { status: 200, headers: { "Content-Type": "application/json" } })));

    render(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    await screen.findByText("指标已更新");
    expect(screen.getByText(/当前 Core 未提供消息链路指标/)).toBeTruthy();
    expect(screen.queryByText("指标不可用")).toBeNull();
    expect(screen.queryByLabelText("端到端调用链")).toBeNull();
  });

  it("projects three controlled tasks without a content-local tablist and keeps one metrics snapshot", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const metrics = validMetrics();
    metrics.usage = {
      overall: [{ lane: "respond", inputTokens: 100, outputTokens: 20, cachedInputTokens: 40, cachedObservedInputTokens: 100, cacheWriteTokens: 0, callCount: 1 }],
      turns: [],
      turnCount: 1,
      truncated: false,
    };
    const stream = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n")); } });
    const paths: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      paths.push(path);
      if (path.includes("/logs/stream")) return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      const payload = path.includes("/traces/") ? validTraceDetail() : metrics;
      return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    const page = render(<Theme><ObservabilityPage token="" view="metrics" /></Theme>);
    await screen.findByText("指标已更新");
    expect(await screen.findByTestId("usage-cached")).toBeTruthy();
    expect(screen.queryByRole("tablist", { name: "可观测诊断任务" })).toBeNull();
    expect(document.querySelector(".observability-tabs")).toBeNull();
    expect(screen.getByRole("heading", { name: "实时指标趋势" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "HTTP 请求" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "模型 Token" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "堆内存" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "累计模型用量" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "消息时延" })).toBeNull();
    expect(screen.queryByText("接收 → Turn 开始")).toBeNull();
    expect(screen.getByRole("heading", { name: "HTTP 路由" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "端到端调用链" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "实时日志" })).toBeNull();
    expect(screen.getAllByText("缓存命中率")).toHaveLength(1);
    expect(screen.queryByText("会话回合", { exact: true })).toBeNull();
    expect(screen.getAllByText("最近会话回合")).toHaveLength(1);

    page.rerender(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    expect(screen.getByRole("heading", { name: "端到端调用链" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "HTTP 路由" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "实时指标趋势" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "累计模型用量" })).toBeNull();

    page.rerender(<Theme><ObservabilityPage token="" view="logs" /></Theme>);
    expect(screen.getByRole("heading", { name: "实时日志" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "HTTP 路由" })).toBeNull();

    expect(paths.filter((path) => path === "/v1/metrics")).toHaveLength(1);
    expect(paths.some((path) => path === "/v1/usage")).toBe(false);
    expect(paths.filter((path) => path.includes("/logs/stream"))).toHaveLength(1);
  });

  it("preserves model filtering and stream state while switching tabs", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const metrics = validMetrics();
    metrics.usage = {
      overall: [{ lane: "respond", inputTokens: 100, outputTokens: 20, cachedInputTokens: 40, cachedObservedInputTokens: 100, cacheWriteTokens: 0, callCount: 1 }],
      turns: [],
      turnCount: 1,
      truncated: false,
    };
    const stream = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n")); } });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/logs/stream")) return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      const payload = path.includes("/traces/") ? validTraceDetail() : metrics;
      return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    const page = render(<Theme><ObservabilityPage token="" view="metrics" /></Theme>);
    await screen.findByText("指标已更新");
    fireEvent.click(screen.getByRole("button", { name: "仅回复" }));
    expect(screen.getByLabelText("本次页面会话指标趋势")).toBeTruthy();
    expect(screen.getByRole("button", { name: "仅回复" }).getAttribute("aria-pressed")).toBe("true");

    page.rerender(<Theme><ObservabilityPage token="" view="logs" /></Theme>);
    const pauseButton = await screen.findByRole("button", { name: "暂停" });
    fireEvent.click(pauseButton);
    expect(screen.getByText("已暂停")).toBeTruthy();

    page.rerender(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    expect(screen.getByRole("heading", { name: "端到端调用链" })).toBeTruthy();
    page.rerender(<Theme><ObservabilityPage token="" view="metrics" /></Theme>);
    expect(screen.getByRole("button", { name: "仅回复" }).getAttribute("aria-pressed")).toBe("true");

    page.rerender(<Theme><ObservabilityPage token="" view="logs" /></Theme>);
    expect(screen.getByText("已暂停")).toBeTruthy();
    expect(screen.getByRole("button", { name: "继续" })).toBeTruthy();
  });

  it("samples metrics every five seconds only while the metrics task is active", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    let metricsRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/logs/stream")) {
        const stream = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n")); } });
        return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      }
      if (path.includes("/traces/")) {
        return new Response(JSON.stringify(validTraceDetail()), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      metricsRequests += 1;
      const metrics = validMetrics();
      metrics.generatedAtUnixMs = metricsRequests * 5_000;
      metrics.http.total = metricsRequests;
      return new Response(JSON.stringify(metrics), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    const page = render(<Theme><ObservabilityPage token="" view="metrics" /></Theme>);
    await screen.findByText("1 / 60 个样本");
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(await screen.findByText("2 / 60 个样本")).toBeTruthy();

    page.rerender(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(metricsRequests).toBe(2);
  });

  it("collapses child spans and keeps the selected call point in the inspector", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const stream = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n")); } });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/logs/stream")) return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      const payload = path.includes("/traces/") ? validTraceDetail() : validMetrics();
      return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    await screen.findByText("模型调用");
    fireEvent.click(screen.getByRole("button", { name: "折叠Turn" }));
    expect(screen.queryByText("模型调用")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "展开Turn" }));
    fireEvent.click(await screen.findByRole("treeitem", { name: /模型调用/ }));
    expect(screen.getByLabelText("Span 详情").textContent).toContain("deepseek-v4-flash");
  });

  it("clears the previous detail when another trace has left the retention window", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const metrics = validMetrics();
    metrics.messages.recent.push({ ...metrics.messages.recent[0], traceId: "msg-4", turnId: "turn-2" });
    const stream = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new TextEncoder().encode("event: ready\ndata: {\"ok\":true}\n\n")); } });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/logs/stream")) return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
      if (path.includes("/traces/msg-4")) return new Response(JSON.stringify({ error: "trace not found" }), { status: 404, headers: { "Content-Type": "application/json" } });
      if (path.includes("/traces/")) return new Response(JSON.stringify(validTraceDetail()), { status: 200, headers: { "Content-Type": "application/json" } });
      return new Response(JSON.stringify(metrics), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    await screen.findByText("模型调用");
    fireEvent.click(screen.getByRole("button", { name: /msg-4/ }));
    expect(await screen.findByText("Trace 已离开保留窗口")).toBeTruthy();
    expect(screen.queryByText("模型调用")).toBeNull();
  });

});

function validMetrics() {
  return {
    generatedAtUnixMs: 1,
    process: { uptimeSeconds: 1, goVersion: "go1.26", goroutines: 2, heapAllocBytes: 3 },
    http: { inFlight: 0, total: 1, status2xx: 1, status4xx: 0, status5xx: 0, routes: [] },
    logs: { retainedEntries: 0, droppedEntries: 0, activeSubscribers: 0, slowSubscriberDisconnects: 0 },
    messages: validMessageMetrics(),
    runtime: { activeBackgroundJobs: 0, eventSubscribers: 0 },
    usage: {
      overall: [] as Array<Record<string, unknown>>,
      turns: [] as Array<Record<string, unknown>>,
      turnCount: 0,
      truncated: false,
    },
  };
}

function validMessageMetrics() {
  const latency = { observations: 1, totalDurationMs: 65, maxDurationMs: 65 };
  return {
    received: 3, sent: 1, directReceived: 1, ambientReceived: 2,
    completed: 1, failed: 0, interrupted: 0, silent: 1, active: 1, droppedEvents: 0,
    latencies: {
      receiveToDecision: latency, receiveToTurn: latency, turnToFirstBeat: latency,
      turnToCompleted: latency, receiveToFirstBeat: latency, receiveToCompleted: latency,
    },
    recent: [{ traceId: "msg-3", source: "ambient", conversationId: "c1", turnId: "t1", status: "completed", receivedAtUnixMs: 1, completedAtUnixMs: 66, totalDurationMs: 65 }],
  };
}

function validTraceDetail() {
  return {
    traceId: "msg-3",
    conversationId: "conversation-1",
    turnId: "turn-1",
    source: "ambient",
    status: "completed",
    startedAtUnixMs: 1,
    endedAtUnixMs: 66,
    durationMs: 65,
    droppedSpanCount: 0,
    truncated: false,
    spans: [
      { spanId: "span-root", parentSpanId: "", operation: "消息处理", category: "message", status: "completed", startedAtUnixMs: 1, endedAtUnixMs: 66, durationMs: 65, attributes: { source: "ambient" } },
      { spanId: "span-turn", parentSpanId: "span-root", operation: "Turn", category: "turn", status: "completed", startedAtUnixMs: 5, endedAtUnixMs: 65, durationMs: 60, attributes: { turn_id: "turn-1" } },
      { spanId: "span-model", parentSpanId: "span-turn", operation: "模型调用", category: "model", status: "completed", startedAtUnixMs: 10, endedAtUnixMs: 60, durationMs: 50, attributes: { lane: "respond", model: "deepseek-v4-flash" } },
    ],
  };
}
