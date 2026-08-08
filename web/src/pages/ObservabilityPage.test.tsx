// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Theme } from "@radix-ui/themes";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ObservabilityPage } from "./ObservabilityPage";

afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "#/");
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
    expect(screen.getByLabelText("Core 持久化指标趋势")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "刷新快照" }));
    await screen.findByText("指标不可用");
    expect(screen.getByText("usage metrics unavailable")).toBeTruthy();
    expect(screen.queryByLabelText("Core 持久化指标趋势")).toBeNull();
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
    metrics.http.routes = [{
      method: "GET", route: "/v1/session/ws", longLived: true,
      requestCount: 1, errorCount: 0, totalDurationMs: 0, maxDurationMs: 0,
    }];
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
    expect(screen.getByRole("heading", { name: "对话接口请求" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "模型 Token" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "公共经验学习" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "回复效果反馈" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "上下文压缩" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "堆内存" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "累计模型用量" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "消息时延" })).toBeNull();
    expect(screen.queryByText("接收 → Turn 开始")).toBeNull();
    expect(screen.getByRole("heading", { name: "对话接口" })).toBeTruthy();
    expect(screen.getByText("/v1/session/ws")).toBeTruthy();
    expect(screen.getByText("长连接")).toBeTruthy();
    expect(screen.queryByText("333,283 ms")).toBeNull();
    const routeTable = screen.getByText("/v1/session/ws").closest("table");
    expect(routeTable?.querySelectorAll("thead th")).toHaveLength(6);
    expect(routeTable?.querySelectorAll("tbody tr:first-child td")).toHaveLength(6);
    expect(screen.queryByRole("heading", { name: "端到端调用链" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "实时日志" })).toBeNull();
    expect(screen.getAllByText("缓存命中率")).toHaveLength(1);
    expect(screen.queryByText("会话回合", { exact: true })).toBeNull();
    expect(screen.getAllByText("最近会话回合")).toHaveLength(1);

    page.rerender(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    expect(screen.getByRole("heading", { name: "端到端调用链" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "对话接口" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "实时指标趋势" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "累计模型用量" })).toBeNull();

    page.rerender(<Theme><ObservabilityPage token="" view="logs" /></Theme>);
    expect(screen.getByRole("heading", { name: "实时日志" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "对话接口" })).toBeNull();

    expect(paths.filter((path) => path === "/v1/metrics")).toHaveLength(1);
    expect(paths.some((path) => path === "/v1/usage")).toBe(false);
    expect(paths.filter((path) => path.includes("/logs/stream"))).toHaveLength(1);
  });

  it("shows the nearest historical sample when a metrics chart is focused or traversed", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const metrics = validMetrics();
    metrics.history = [metricHistoryPoint(10_000, 10, 1, 1_000), metricHistoryPoint(20_000, 20, 2, 15_000)];
    metrics.history[0].feedbackRegistered = 3;
    metrics.history[0].feedbackSuperseded = 1;
    metrics.history[0].feedbackSucceeded = 1;
    metrics.history[1].feedbackRegistered = 8;
    metrics.history[1].feedbackSuperseded = 2;
    metrics.history[1].feedbackSucceeded = 6;
    metrics.history[0].feedbackModelCalls = 1;
    metrics.history[0].feedbackInputTokens = 500;
    metrics.history[0].feedbackCachedObservedInputTokens = 400;
    metrics.history[0].feedbackCachedInputTokens = 300;
    metrics.history[0].feedbackCacheWriteTokens = 25;
    metrics.history[0].feedbackOutputTokens = 40;
    metrics.history[1].feedbackModelCalls = 4;
    metrics.history[1].feedbackInputTokens = 2_000;
    metrics.history[1].feedbackCachedObservedInputTokens = 1_800;
    metrics.history[1].feedbackCachedInputTokens = 1_400;
    metrics.history[1].feedbackCacheWriteTokens = 100;
    metrics.history[1].feedbackOutputTokens = 160;
    metrics.history[0].compactionL1Applied = 2;
    metrics.history[0].compactionL2Applied = 1;
    metrics.history[1].compactionL1Applied = 5;
    metrics.history[1].compactionL2Applied = 3;
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(metrics), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    render(<Theme><ObservabilityPage token="" view="metrics" /></Theme>);
    const chart = await screen.findByRole("img", { name: /对话接口请求/ });
    vi.spyOn(chart, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, width: 640, height: 220, top: 0, right: 640, bottom: 220, left: 0,
      toJSON: () => ({}),
    });
    const tooltip = chart.parentElement?.querySelector<HTMLElement>(".metric-chart-tooltip");
    const readout = chart.closest(".metric-trend-chart")?.querySelector<HTMLElement>(".metric-trend-readout");

    fireEvent.pointerMove(chart, { clientX: 50 });
    expect(tooltip?.hidden).toBe(false);
    expect(tooltip?.textContent).toContain("10");
    expect(tooltip?.textContent).toContain("1");
    expect(tooltip?.textContent).toContain("历史 Core");
    expect(readout?.textContent).toContain("10");
    expect(readout?.textContent).toContain("1");
    fireEvent.pointerLeave(chart);
    expect(tooltip?.hidden).toBe(true);
    expect(readout?.textContent).toContain("最新样本");

    fireEvent.focus(chart);
    expect(tooltip?.hidden).toBe(false);
    expect(tooltip?.textContent).toContain("20");
    expect(tooltip?.textContent).toContain("2");
    expect(tooltip?.textContent).toContain("当前 Core");
    expect(readout?.textContent).toContain("20");
    expect(readout?.textContent).toContain("2");
    expect(readout?.textContent).toContain("当前 Core");
    expect(document.querySelectorAll(".metric-chart-process-boundary")).toHaveLength(10);

    fireEvent.keyDown(chart, { key: "ArrowLeft" });
    expect(tooltip?.textContent).toContain("10");
    expect(tooltip?.textContent).toContain("1");
    expect(tooltip?.textContent).toContain("历史 Core");
    expect(readout?.textContent).toContain("10");
    expect(readout?.textContent).toContain("1");
    expect(readout?.textContent).toContain("历史 Core");

    fireEvent.pointerLeave(chart);
    expect(tooltip?.hidden).toBe(true);
    expect(readout?.textContent).toContain("20");
    expect(readout?.textContent).toContain("2");
    expect(readout?.textContent).toContain("最新样本");

    const feedbackChart = screen.getByRole("img", { name: /回复效果反馈/ });
    vi.spyOn(feedbackChart, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, width: 640, height: 220, top: 0, right: 640, bottom: 220, left: 0,
      toJSON: () => ({}),
    });
    fireEvent.focus(feedbackChart);
    const feedbackReadout = feedbackChart.closest(".metric-trend-chart")?.querySelector<HTMLElement>(".metric-trend-readout");
    expect(feedbackReadout?.textContent).toContain("注册8");
    expect(feedbackReadout?.textContent).toContain("提前结算2");
    expect(feedbackReadout?.textContent).toContain("成功6");
    expect(feedbackReadout?.textContent).toContain("评估调用4");
    fireEvent.keyDown(feedbackChart, { key: "ArrowLeft" });
    expect(feedbackReadout?.textContent).toContain("注册3");
    expect(feedbackReadout?.textContent).toContain("提前结算1");
    expect(feedbackReadout?.textContent).toContain("成功1");
    expect(feedbackReadout?.textContent).toContain("评估调用1");

    const feedbackUsageChart = screen.getByRole("img", { name: /Feedback 模型用量/ });
    vi.spyOn(feedbackUsageChart, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, width: 640, height: 220, top: 0, right: 640, bottom: 220, left: 0,
      toJSON: () => ({}),
    });
    fireEvent.focus(feedbackUsageChart);
    const feedbackUsageReadout = feedbackUsageChart.closest(".metric-trend-chart")?.querySelector<HTMLElement>(".metric-trend-readout");
    expect(feedbackUsageReadout?.textContent).toContain("输入2,000");
    expect(feedbackUsageReadout?.textContent).toContain("缓存观测输入1,800");
    expect(feedbackUsageReadout?.textContent).toContain("缓存命中1,400");
    fireEvent.keyDown(feedbackUsageChart, { key: "ArrowLeft" });
    expect(feedbackUsageReadout?.textContent).toContain("输入500");
    expect(feedbackUsageReadout?.textContent).toContain("缓存观测输入400");
    expect(feedbackUsageReadout?.textContent).toContain("缓存命中300");

    const compactionChart = screen.getByRole("img", { name: /上下文压缩/ });
    vi.spyOn(compactionChart, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, width: 640, height: 220, top: 0, right: 640, bottom: 220, left: 0,
      toJSON: () => ({}),
    });
    fireEvent.focus(compactionChart);
    const compactionReadout = compactionChart.closest(".metric-trend-chart")?.querySelector<HTMLElement>(".metric-trend-readout");
    expect(compactionReadout?.textContent).toContain("L1 Tool Result5");
    expect(compactionReadout?.textContent).toContain("L2 记忆覆盖3");
    fireEvent.keyDown(compactionChart, { key: "ArrowLeft" });
    expect(compactionReadout?.textContent).toContain("L1 Tool Result2");
    expect(compactionReadout?.textContent).toContain("L2 记忆覆盖1");
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
    window.history.replaceState(null, "", "#/metrics");
    fireEvent.click(screen.getByRole("button", { name: "仅回复" }));
    expect(screen.getByLabelText("Core 持久化指标趋势")).toBeTruthy();
    expect(screen.getByRole("button", { name: "仅回复" }).getAttribute("aria-pressed")).toBe("true");

    page.rerender(<Theme><ObservabilityPage token="" view="logs" /></Theme>);
    const pauseButton = await screen.findByRole("button", { name: "暂停" });
    fireEvent.click(pauseButton);
    expect(screen.getByText("已暂停")).toBeTruthy();

    page.rerender(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    expect(screen.getByRole("heading", { name: "端到端调用链" })).toBeTruthy();
    await waitFor(() => expect(window.location.hash).toContain("#/tracing?traceId="));
    window.history.replaceState(null, "", "#/metrics");
    page.rerender(<Theme><ObservabilityPage token="" view="metrics" /></Theme>);
    expect(screen.getByRole("button", { name: "仅回复" }).getAttribute("aria-pressed")).toBe("true");
    await waitFor(() => expect(window.location.hash).toBe("#/metrics"));

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
      if (path.includes("/turns/turn-1/runtime")) {
        return new Response(JSON.stringify({ conversationId: "conversation-1", turnId: "turn-1", events: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
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

  it("searches by messageId, deep-links the selected trace, and reuses Turn runtime detail", async () => {
    window.history.replaceState(null, "", "#/tracing?traceId=msg-linked");
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/traces?messageId=qq-77")) {
        return new Response(JSON.stringify({
          messageId: "qq-77",
          traces: [{ traceId: "msg-search", messageId: "qq-77", source: "ambient", conversationId: "conversation-9", turnId: "turn-9", status: "completed", receivedAtUnixMs: 10, completedAtUnixMs: 30, totalDurationMs: 20 }],
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces/qq-77")) {
        return new Response(JSON.stringify({ error: "trace not found" }), { status: 404, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/turns/turn-9/runtime")) {
        return new Response(JSON.stringify({ conversationId: "conversation-9", turnId: "turn-9", events: [runtimeToolEvent()] }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/turns/turn-linked/runtime")) {
        return new Response(JSON.stringify({ conversationId: "conversation-linked", turnId: "turn-linked", events: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces/msg-search")) {
        return new Response(JSON.stringify({ ...validTraceDetail(), traceId: "msg-search", messageId: "qq-77", conversationId: "conversation-9", turnId: "turn-9" }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces/msg-linked")) {
        return new Response(JSON.stringify({ ...validTraceDetail(), traceId: "msg-linked", conversationId: "conversation-linked", turnId: "turn-linked" }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify(validMetrics()), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    expect((await screen.findAllByText("msg-linked")).length).toBeGreaterThan(0);
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/traces/msg-linked"))).toBe(true);

    fireEvent.change(screen.getByLabelText("按 traceId 或 messageId 搜索 Trace"), { target: { value: "qq-77" } });
    fireEvent.click(screen.getByRole("button", { name: "搜索 Trace 关联标识" }));
    expect(await screen.findByText(/messageId qq-77/)).toBeTruthy();
    expect(window.location.hash).toBe("#/tracing?traceId=msg-search");
    expect(await screen.findByText("Turn 运行明细")).toBeTruthy();
    fireEvent.click(await screen.findByRole("button", { name: /工具执行/ }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("苍彼四重奏");
    expect(dialog.textContent).toContain("最终注入上下文");
  });

  it("accepts a visible traceId in the unified lookup and deduplicates both exact branches", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/traces?messageId=msg-copy")) {
        return new Response(JSON.stringify({
          messageId: "msg-copy",
          traces: [{ traceId: "msg-copy", messageId: "msg-copy", source: "ambient", conversationId: "conversation-copy", turnId: "", status: "silent", receivedAtUnixMs: 10, completedAtUnixMs: 30, totalDurationMs: 20 }],
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces/msg-copy")) {
        return new Response(JSON.stringify(ambientParticipationTraceDetail()), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces/msg-3")) {
        return new Response(JSON.stringify(validTraceDetail()), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify(validMetrics()), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    await screen.findByText("模型调用");
    fireEvent.change(screen.getByLabelText("按 traceId 或 messageId 搜索 Trace"), { target: { value: "msg-copy" } });
    fireEvent.click(screen.getByRole("button", { name: "搜索 Trace 关联标识" }));

    await waitFor(() => expect(window.location.hash).toBe("#/tracing?traceId=msg-copy"));
    expect(screen.getByText("关联查询结果")).toBeTruthy();
    expect(screen.getAllByText("msg-copy").length).toBeGreaterThan(0);
    expect(document.querySelectorAll(".trace-summary")).toHaveLength(1);
    expect(screen.getByText("Trace ID", { selector: ".trace-detail-title > span" })).toBeTruthy();
    expect(screen.getAllByText(/外部 messageId msg-copy/)).toHaveLength(2);
    expect(screen.getByText("参与上下文准备")).toBeTruthy();
    expect(screen.getByText("参与模型调用")).toBeTruthy();
    expect(screen.getByText("参与结果编译")).toBeTruthy();
    expect(screen.getByText("未创建 Turn")).toBeTruthy();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/traces?messageId=msg-copy"))).toBe(true);
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/traces/msg-copy"))).toBe(true);
  });

  it("does not keep an old trace selected for multi-match or empty lookup results", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined, removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/traces?messageId=external-many")) {
        return new Response(JSON.stringify({
          messageId: "external-many",
          traces: [
            { traceId: "trace-a", messageId: "external-many", source: "ambient", conversationId: "c-a", turnId: "", status: "silent", receivedAtUnixMs: 10, completedAtUnixMs: 20, totalDurationMs: 10 },
            { traceId: "trace-b", messageId: "external-many", source: "ambient", conversationId: "c-b", turnId: "", status: "failed", receivedAtUnixMs: 30, completedAtUnixMs: 40, totalDurationMs: 10 },
          ],
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces?messageId=not-found")) {
        return new Response(JSON.stringify({ messageId: "not-found", traces: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces?messageId=broken")) {
        return new Response(JSON.stringify({ error: "trace storage unavailable" }), { status: 503, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces/external-many") || path.includes("/traces/not-found")) {
        return new Response(JSON.stringify({ error: "trace not found" }), { status: 404, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces/broken")) {
        return new Response(JSON.stringify({ error: "trace not found" }), { status: 404, headers: { "Content-Type": "application/json" } });
      }
      if (path.includes("/traces/msg-3")) {
        return new Response(JSON.stringify(validTraceDetail()), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify(validMetrics()), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    render(<Theme><ObservabilityPage token="" view="tracing" /></Theme>);
    await screen.findByText("模型调用");
    const input = screen.getByLabelText("按 traceId 或 messageId 搜索 Trace");
    fireEvent.change(input, { target: { value: "broken" } });
    fireEvent.click(screen.getByRole("button", { name: "搜索 Trace 关联标识" }));
    expect(await screen.findByText(/messageId 查询失败/)).toBeTruthy();
    expect(window.location.hash).toBe("#/tracing");
    await waitFor(() => expect(screen.queryByText("模型调用")).toBeNull());

    fireEvent.change(input, { target: { value: "external-many" } });
    fireEvent.click(screen.getByRole("button", { name: "搜索 Trace 关联标识" }));
    expect(await screen.findByText("选择一条 Trace")).toBeTruthy();
    expect(document.querySelectorAll(".trace-summary")).toHaveLength(2);
    expect(window.location.hash).toBe("#/tracing");
    await waitFor(() => expect(screen.queryByText("模型调用")).toBeNull());

    fireEvent.change(input, { target: { value: "not-found" } });
    fireEvent.click(screen.getByRole("button", { name: "搜索 Trace 关联标识" }));
    expect((await screen.findAllByText("没有匹配 Trace")).length).toBeGreaterThan(0);
    expect(document.querySelectorAll(".trace-summary")).toHaveLength(0);
    expect(window.location.hash).toBe("#/tracing");
  });

});

function validMetrics() {
  return {
    generatedAtUnixMs: 1,
    process: { uptimeSeconds: 1, goVersion: "go1.26", goroutines: 2, heapAllocBytes: 3 },
    http: { inFlight: 0, total: 1, status2xx: 1, status4xx: 0, status5xx: 0, routes: [] as Array<Record<string, unknown>> },
    logs: { retainedEntries: 0, droppedEntries: 0, activeSubscribers: 0, slowSubscriberDisconnects: 0 },
    messages: validMessageMetrics(),
    runtime: {
      activeBackgroundJobs: 0,
      eventSubscribers: 0,
      agentLoop: {
        compaction: { l1Applied: 0, l2Applied: 0, l3Applied: 0, failed: 0 },
      },
      experience: {
        learning: { enqueued: 0, dropped: 0, succeeded: 0, failed: 0 },
        feedback: {
          registered: 0, superseded: 0, dropped: 0, succeeded: 0, failed: 0,
          modelCalls: 0, inputTokens: 0, cachedObservedInputTokens: 0,
          cachedInputTokens: 0, cacheWriteTokens: 0, outputTokens: 0,
        },
        cacheIdentityVersion: "prompt-cache-v2",
      },
    },
    usage: {
      overall: [] as Array<Record<string, unknown>>,
      turns: [] as Array<Record<string, unknown>>,
      turnCount: 0,
      truncated: false,
    },
    history: [] as Array<ReturnType<typeof metricHistoryPoint>>,
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

function ambientParticipationTraceDetail() {
  return {
    traceId: "msg-copy",
    messageId: "msg-copy",
    conversationId: "conversation-copy",
    turnId: "",
    source: "ambient",
    status: "silent",
    startedAtUnixMs: 10,
    endedAtUnixMs: 30,
    durationMs: 20,
    droppedSpanCount: 0,
    truncated: false,
    spans: [
      { spanId: "ambient-root", parentSpanId: "", operation: "消息处理", category: "message", status: "silent", startedAtUnixMs: 10, endedAtUnixMs: 30, durationMs: 20, attributes: { source: "ambient" } },
      { spanId: "ambient-participation", parentSpanId: "ambient-root", operation: "参与判断", category: "participation", status: "completed", startedAtUnixMs: 10, endedAtUnixMs: 30, durationMs: 20, attributes: { action: "silent" } },
      { spanId: "ambient-context", parentSpanId: "ambient-participation", operation: "参与上下文准备", category: "context", status: "completed", startedAtUnixMs: 10, endedAtUnixMs: 12, durationMs: 2, attributes: { itemCount: "6" } },
      { spanId: "ambient-model", parentSpanId: "ambient-participation", operation: "参与模型调用", category: "model", status: "completed", startedAtUnixMs: 12, endedAtUnixMs: 29, durationMs: 17, attributes: { lane: "participate", attempt: "1", inputTokens: "31" } },
      { spanId: "ambient-compile", parentSpanId: "ambient-participation", operation: "参与结果编译", category: "compile", status: "completed", startedAtUnixMs: 29, endedAtUnixMs: 30, durationMs: 1, attributes: { attempt: "1", action: "silent" } },
    ],
  };
}

function runtimeToolEvent() {
  return {
    sequence: 1,
    eventType: "tool",
    state: "planning",
    metadata: {
      tool: "web_search",
      phase: "model_driven",
      status: "ok",
      detail: {
        version: "v1",
        arguments: { query: "苍彼四重奏" },
        receipt: { status: "ok" },
        result: { personalMemories: [], knowledge: [], socialMemories: { entries: [] }, semanticStatus: "ready" },
        mergedContext: { personalMemories: [], knowledge: [], socialMemories: { entries: [] }, semanticStatus: "ready" },
      },
    },
    createdAtUnixMs: 20,
  };
}

function metricHistoryPoint(timestampUnixMs: number, httpTotal: number, httpInFlight: number, processStartedAtUnixMs = 1) {
  return {
    timestampUnixMs,
    processStartedAtUnixMs,
    httpTotal,
    httpInFlight,
    httpStatus4xx: 0,
    httpStatus5xx: 0,
    messagesReceived: 0,
    messagesSent: 0,
    messagesActive: 0,
    messagesFailed: 0,
    inputTokens: 0,
    cachedInputTokens: 0,
    outputTokens: 0,
    modelCalls: 0,
    learningEnqueued: 0,
    learningSucceeded: 0,
    learningFailed: 0,
    learningDropped: 0,
    feedbackRegistered: 0,
    feedbackSuperseded: 0,
    feedbackSucceeded: 0,
    feedbackFailed: 0,
    feedbackDropped: 0,
    feedbackModelCalls: 0,
    feedbackInputTokens: 0,
    feedbackCachedObservedInputTokens: 0,
    feedbackCachedInputTokens: 0,
    feedbackCacheWriteTokens: 0,
    feedbackOutputTokens: 0,
    compactionL1Applied: 0,
    compactionL2Applied: 0,
    compactionL3Applied: 0,
    compactionFailed: 0,
    goroutines: 1,
    backgroundJobs: 0,
    eventSubscribers: 0,
    logSubscribers: 0,
    heapMiB: 1,
  };
}
