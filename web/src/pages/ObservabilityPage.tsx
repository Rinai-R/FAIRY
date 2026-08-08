import { ChevronDownIcon, ChevronRightIcon, PauseIcon, PlayIcon, ReloadIcon } from "@radix-ui/react-icons";
import { Button, Select, Text, TextField } from "@radix-ui/themes";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, api } from "../api";
import { EmptyState, InlineNotice, PageHeader, SectionHeading } from "../components/ui";
import {
  METRICS_POLL_INTERVAL_MS,
  appendMetricTrend,
  buildLineGeometry,
  chartDomainMax,
  projectMetricsTrend,
  type MetricTrendKey,
  type MetricsTrendPoint,
} from "../metricsTrend";
import {
  appendPendingLogs,
  followLogs,
  mergeVisibleLogs,
  parseMetrics,
  parseTraceDetail,
  type LogEntry,
  type LogLevel,
  type MessageTrace,
  type MetricsSnapshot,
  type TraceDetail,
  type TraceSpan,
} from "../observability";
import {
  USAGE_LANE_FILTER_ALL,
  USAGE_LANE_FILTER_RESPOND,
  aggregateUsage,
  formatHitRate,
  formatTokenCount,
  formatUsageTime,
  turnMatchesLane,
  usageHitRate,
  type UsageLaneFilter,
  type UsageReport,
} from "../usageReport";

type StreamStatus = "connecting" | "live" | "paused" | "disconnected" | "error";
export type ObservabilityView = "metrics" | "tracing" | "logs";

const OBSERVABILITY_COPY: Record<ObservabilityView, { title: string; description: string }> = {
  metrics: { title: "指标", description: "查看 Core 运行状态、模型调用、Token 与缓存利用。" },
  tracing: { title: "链路跟踪", description: "沿完整调用链查看每个 Span 的父子关系、开始、结束与耗时。" },
  logs: { title: "日志", description: "筛选并流式诊断当前 Core 日志。" },
};

export function ObservabilityPage({ token, view }: { token: string; view: ObservabilityView }) {
  const copy = OBSERVABILITY_COPY[view];
  const [metrics, setMetrics] = useState<MetricsSnapshot | null>(null);
  const [metricsTrend, setMetricsTrend] = useState<MetricsTrendPoint[]>([]);
  const [metricsError, setMetricsError] = useState("");
  const [metricsLoading, setMetricsLoading] = useState(true);
  const requestVersionRef = useRef(0);
  const requestInFlightRef = useRef(false);

  const refreshMetrics = useCallback(async (showLoading: boolean) => {
    if (requestInFlightRef.current) return;
    requestInFlightRef.current = true;
    const requestVersion = ++requestVersionRef.current;
    setMetricsError("");
    if (showLoading) setMetricsLoading(true);
    try {
      const snapshot = parseMetrics(await api<unknown>("/metrics"));
      if (requestVersion !== requestVersionRef.current) return;
      setMetrics(snapshot);
      setMetricsTrend((history) => appendMetricTrend(history, projectMetricsTrend(snapshot)));
    } catch (error: unknown) {
      if (requestVersion !== requestVersionRef.current) return;
      setMetrics(null);
      setMetricsTrend([]);
      setMetricsError(errorMessage(error));
    } finally {
      if (requestVersion === requestVersionRef.current) {
        requestInFlightRef.current = false;
        setMetricsLoading(false);
      }
    }
  }, [token]);

  useEffect(() => {
    void refreshMetrics(true);
    return () => {
      requestVersionRef.current += 1;
      requestInFlightRef.current = false;
    };
  }, [refreshMetrics]);

  useEffect(() => {
    if (view !== "metrics") return;
    const timer = window.setInterval(() => void refreshMetrics(false), METRICS_POLL_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [refreshMetrics, view]);

  return (
    <section className="observability-page">
      <PageHeader
        title={copy.title}
        description={copy.description}
        action={
          <div className="observability-header-actions">
            <div className={`snapshot-status ${metricsError ? "error" : metrics ? "ready" : "loading"}`} aria-label="指标快照状态">
              <strong>{metricsLoading ? "读取指标" : metricsError ? "指标不可用" : metrics ? "指标已更新" : "等待指标"}</strong>
              {metrics ? <span>{formatDateTime(metrics.generatedAtUnixMs)} · {metrics.process.goVersion}</span> : null}
            </div>
            <Button variant="soft" disabled={metricsLoading} onClick={() => void refreshMetrics(true)}>
              <ReloadIcon /> 刷新快照
            </Button>
          </div>
        }
      />

      {metricsError ? (
        <InlineNotice tone="error" title="指标快照读取失败">{metricsError}</InlineNotice>
      ) : null}

      <div className="observability-workbench">
        <div
          id="observability-panel-metrics"
          className="observability-panel"
          role="region"
          aria-label="指标内容"
          hidden={view !== "metrics"}
        >
          {metricsLoading && !metrics ? <MetricsLoading /> : null}
          {metrics ? (
            <>
              <MetricsTrendDashboard history={metricsTrend} messagesAvailable={metrics.messagesAvailable} />
              <HttpRoutes metrics={metrics} />
              <UsageDashboard report={metrics.usage} />
            </>
          ) : null}
        </div>

        <div
          id="observability-panel-tracing"
          className="observability-panel"
          role="region"
          aria-label="链路跟踪内容"
          hidden={view !== "tracing"}
        >
          {metricsLoading && !metrics ? <MetricsLoading /> : null}
          {metrics ? (
            <>
              {!metrics.messagesAvailable ? (
                <InlineNotice tone="warning">当前 Core 未提供消息链路指标，请重启 Core 后查看。</InlineNotice>
              ) : <TraceWorkbench metrics={metrics} token={token} />}
            </>
          ) : null}
        </div>

        <div
          id="observability-panel-logs"
          className="observability-panel"
          role="region"
          aria-label="日志内容"
          hidden={view !== "logs"}
        >
          <LiveLogPanel token={token} />
        </div>
      </div>
    </section>
  );
}

function MetricsLoading() {
  return (
    <div className="metrics-loading" aria-label="正在读取指标">
      {Array.from({ length: 6 }, (_, index) => <span key={index} />)}
    </div>
  );
}

type TrendSeries = { key: MetricTrendKey; label: string; color: string; decimals?: number };

const TREND_CHARTS: Array<{
  id: string;
  className: string;
  title: string;
  description: string;
  series: TrendSeries[];
}> = [
  {
    id: "http-traffic", className: "wide", title: "HTTP 请求", description: "累计请求与当前处理中请求",
    series: [
      { key: "httpTotal", label: "累计请求", color: "#2878d0" },
      { key: "httpInFlight", label: "处理中", color: "#7d93aa" },
    ],
  },
  {
    id: "http-errors", className: "narrow", title: "HTTP 错误", description: "客户端错误与服务端错误",
    series: [
      { key: "httpStatus4xx", label: "4xx", color: "#b37622" },
      { key: "httpStatus5xx", label: "5xx", color: "#b84855" },
    ],
  },
  {
    id: "messages", className: "half", title: "消息活动", description: "累计收发与当前处理状态",
    series: [
      { key: "messagesReceived", label: "收到", color: "#2878d0" },
      { key: "messagesSent", label: "发送", color: "#3c8b72" },
      { key: "messagesActive", label: "处理中", color: "#7d93aa" },
      { key: "messagesFailed", label: "失败", color: "#b84855" },
    ],
  },
  {
    id: "model-tokens", className: "half", title: "模型 Token", description: "全部 lane 的累计模型用量",
    series: [
      { key: "inputTokens", label: "输入", color: "#2878d0" },
      { key: "cachedInputTokens", label: "缓存命中", color: "#3c8b72" },
      { key: "outputTokens", label: "输出", color: "#7059ad" },
    ],
  },
  {
    id: "runtime", className: "wide", title: "运行任务", description: "Go 进程和后台工作负载",
    series: [
      { key: "goroutines", label: "Goroutine", color: "#2878d0" },
      { key: "backgroundJobs", label: "后台任务", color: "#7059ad" },
      { key: "eventSubscribers", label: "事件订阅", color: "#3c8b72" },
      { key: "logSubscribers", label: "日志订阅", color: "#7d93aa" },
    ],
  },
  {
    id: "heap", className: "narrow", title: "堆内存", description: "当前 Go heap 分配",
    series: [{ key: "heapMiB", label: "Heap MiB", color: "#2878d0", decimals: 1 }],
  },
];

function MetricsTrendDashboard({ history, messagesAvailable }: { history: MetricsTrendPoint[]; messagesAvailable: boolean }) {
  return (
    <section className="observability-section metrics-trend-dashboard" aria-label="本次页面会话指标趋势">
      <SectionHeading
        title="实时指标趋势"
        description="每 5 秒采样当前 Core 快照，曲线仅覆盖本次页面会话。"
        aside={`${history.length} / 60 个样本`}
      />
      {!messagesAvailable ? <InlineNotice tone="warning">当前 Core 未提供消息指标，消息曲线以零基线显示。</InlineNotice> : null}
      <div className="metrics-trend-grid">
        {TREND_CHARTS.map((chart) => <MetricTrendChart key={chart.id} {...chart} history={history} />)}
      </div>
    </section>
  );
}

function MetricTrendChart({
  id,
  className,
  title,
  description,
  series,
  history,
}: {
  id: string;
  className: string;
  title: string;
  description: string;
  series: TrendSeries[];
  history: MetricsTrendPoint[];
}) {
  const width = 640;
  const height = 220;
  const domainMax = chartDomainMax(history, series.map((item) => item.key));
  const latest = history.at(-1);
  const titleID = `metric-chart-${id}-title`;
  const descriptionID = `metric-chart-${id}-description`;
  return (
    <article className={`metric-trend-chart ${className}`} aria-labelledby={titleID}>
      <header className="metric-trend-heading">
        <div><h3 id={titleID}>{title}</h3><p>{description}</p></div>
        <div className="metric-trend-legend" aria-label={`${title}图例`}>
          {series.map((item) => (
            <span key={item.key}>
              <i style={{ backgroundColor: item.color }} aria-hidden="true" />
              <span>{item.label}</span>
              <strong>{formatTrendValue(latest?.[item.key] ?? 0, item.decimals)}</strong>
            </span>
          ))}
        </div>
      </header>
      <div className="metric-chart-canvas">
        <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-labelledby={`${titleID} ${descriptionID}`}>
          <desc id={descriptionID}>{history.length < 2 ? `${title}当前只有一个样本，正在积累趋势。` : `${title}包含本次页面会话最近${history.length}个样本。`}</desc>
          {[0, 0.25, 0.5, 0.75, 1].map((ratio) => {
            const y = 12 + ratio * 184;
            const value = domainMax * (1 - ratio);
            return (
              <g key={ratio} className="metric-chart-gridline">
                <line x1="48" x2="628" y1={y} y2={y} />
                <text x="40" y={y + 4} textAnchor="end">{formatAxisValue(value)}</text>
              </g>
            );
          })}
          {series.map((item) => {
            const geometry = buildLineGeometry(history.map((point) => point[item.key]), domainMax, width, height);
            const lastPoint = geometry.points.at(-1);
            return (
              <g key={item.key} className="metric-chart-series">
                <path d={geometry.path} stroke={item.color} />
                {lastPoint ? <circle cx={lastPoint.x} cy={lastPoint.y} r="3.5" fill={item.color} /> : null}
              </g>
            );
          })}
        </svg>
        <div className="metric-chart-time" aria-hidden="true">
          <span>{history[0] ? formatTime(history[0].timestampUnixMs) : "等待样本"}</span>
          <span>{latest ? formatTime(latest.timestampUnixMs) : "等待样本"}</span>
        </div>
      </div>
      {history.length < 2 ? <p className="metric-chart-pending">正在积累趋势，下一次采样后连接折线。</p> : null}
    </article>
  );
}

function HttpRoutes({ metrics }: { metrics: MetricsSnapshot }) {
  return (
    <section className="observability-section route-observability">
      <SectionHeading title="HTTP 路由" description="按路由模板聚合请求与耗时，避免资源标识造成高基数。" aside={`${metrics.http.routes.length} 条路由`} />
      {metrics.http.routes.length === 0 ? (
        <EmptyState title="还没有路由观测" description="Core 收到管理请求后，这里会显示调用与耗时。" />
      ) : (
        <div className="table-scroll">
          <table className="data-table route-table">
            <thead><tr><th>方法</th><th>路由</th><th>请求</th><th>错误</th><th>平均耗时</th><th>最大耗时</th></tr></thead>
            <tbody>{metrics.http.routes.map((route) => (
              <tr key={`${route.method}-${route.route}`}>
                <td><code>{route.method}</code></td><td><code>{route.route}</code></td><td>{formatNumber(route.requestCount)}</td><td>{formatNumber(route.errorCount)}</td>
                <td>{route.requestCount ? `${formatNumber(Math.round(route.totalDurationMs / route.requestCount))} ms` : "N/A"}</td><td>{route.requestCount ? `${formatNumber(route.maxDurationMs)} ms` : "N/A"}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      )}
    </section>
  );
}

type TraceDetailState = "idle" | "loading" | "ready" | "missing" | "unsupported" | "error";
type TraceRow = { span: TraceSpan; depth: number; hasChildren: boolean };

function TraceWorkbench({ metrics, token }: { metrics: MetricsSnapshot; token: string }) {
  const traces = metrics.messages.recent;
  const [selectedTraceID, setSelectedTraceID] = useState(traces[0]?.traceId ?? "");
  const [detail, setDetail] = useState<TraceDetail | null>(null);
  const [detailState, setDetailState] = useState<TraceDetailState>(traces.length > 0 ? "loading" : "idle");
  const [detailError, setDetailError] = useState("");
  const [detailRevision, setDetailRevision] = useState(0);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [selectedSpanID, setSelectedSpanID] = useState("");
  const [clock, setClock] = useState(Date.now());

  useEffect(() => {
    if (traces.length === 0) {
      setSelectedTraceID("");
      return;
    }
    if (!traces.some((trace) => trace.traceId === selectedTraceID)) {
      setSelectedTraceID(traces[0].traceId);
    }
  }, [selectedTraceID, traces]);

  useEffect(() => {
    if (!selectedTraceID) {
      setDetail(null);
      setDetailState("idle");
      return;
    }
    let active = true;
    setDetail(null);
    setDetailError("");
    setDetailState("loading");
    setCollapsed(new Set());
    setSelectedSpanID("");
    api<unknown>(`/traces/${encodeURIComponent(selectedTraceID)}`)
      .then((value) => {
        if (!active) return;
        const parsed = parseTraceDetail(value);
        if (parsed.traceId !== selectedTraceID) throw new Error("Trace 详情与当前选择不一致");
        setDetail(parsed);
        setSelectedSpanID(parsed.spans.find((span) => !span.parentSpanId)?.spanId ?? parsed.spans[0]?.spanId ?? "");
        setDetailState("ready");
      })
      .catch((error: unknown) => {
        if (!active) return;
        setDetail(null);
        setDetailError(errorMessage(error));
        if (error instanceof ApiError && error.status === 404) {
          setDetailState(error.message === "trace not found" ? "missing" : "unsupported");
        } else {
          setDetailState("error");
        }
      });
    return () => { active = false; };
  }, [detailRevision, selectedTraceID, token]);

  useEffect(() => {
    if (!detail || detail.endedAtUnixMs > 0) return;
    setClock(Date.now());
    const timer = window.setInterval(() => setClock(Date.now()), 500);
    return () => window.clearInterval(timer);
  }, [detail]);

  const rows = useMemo(() => buildTraceRows(detail?.spans ?? [], collapsed), [collapsed, detail]);
  const selectedSpan = detail?.spans.find((span) => span.spanId === selectedSpanID) ?? null;
  const traceEnd = detail?.endedAtUnixMs || clock;
  const traceDuration = detail ? Math.max(1, traceEnd - detail.startedAtUnixMs) : 1;

  function toggleCollapsed(spanID: string) {
    setCollapsed((current) => {
      const next = new Set(current);
      if (next.has(spanID)) next.delete(spanID); else next.add(spanID);
      return next;
    });
  }

  return (
    <section className="observability-section trace-workbench" aria-label="端到端调用链">
      <SectionHeading
        title="端到端调用链"
        description="选择一条 Trace，沿父子 Span 查看每个调用点的开始、结束与耗时。"
        aside={`${traces.length} 条最近 Trace`}
      />
      {traces.length === 0 ? (
        <EmptyState title="还没有可查看的 Trace" description="完成一次消息处理后，这里会出现端到端调用链。" />
      ) : (
        <div className="trace-explorer">
          <aside className="trace-browser" aria-label="最近 Trace">
            <div className="trace-browser-heading">
              <strong>最近 Trace</strong>
              <span>按接收时间倒序</span>
            </div>
            <div className="trace-browser-list">
              {traces.map((trace) => (
                <TraceSummaryButton
                  key={trace.traceId}
                  trace={trace}
                  selected={trace.traceId === selectedTraceID}
                  onSelect={() => setSelectedTraceID(trace.traceId)}
                />
              ))}
            </div>
          </aside>

          <div className="trace-detail-shell">
            {detailState === "loading" ? <TraceDetailLoading /> : null}
            {detailState === "missing" ? (
              <TraceFailure title="Trace 已离开保留窗口" description="刷新指标快照后重新选择一条最近 Trace。" onRetry={() => setDetailRevision((value) => value + 1)} />
            ) : null}
            {detailState === "unsupported" ? (
              <TraceFailure title="当前 Core 不支持调用链详情" description="请更新或重启 Core 后重新加载。" onRetry={() => setDetailRevision((value) => value + 1)} />
            ) : null}
            {detailState === "error" ? (
              <TraceFailure title="调用链读取失败" description={detailError} onRetry={() => setDetailRevision((value) => value + 1)} />
            ) : null}
            {detailState === "ready" && detail ? (
              <>
                <header className="trace-detail-header">
                  <div>
                    <div className="trace-detail-title"><code>{detail.traceId}</code><span className={`trace-status ${detail.status}`}>{statusLabel(detail.status)}</span></div>
                    <span>{sourceLabel(detail.source)}，Conversation {shortID(detail.conversationId)}{detail.turnId ? `，Turn ${shortID(detail.turnId)}` : ""}</span>
                  </div>
                  <dl className="trace-summary-metrics">
                    <div><dt>开始</dt><dd>{formatDateTimePrecise(detail.startedAtUnixMs)}</dd></div>
                    <div><dt>结束</dt><dd>{detail.endedAtUnixMs ? formatDateTimePrecise(detail.endedAtUnixMs) : "进行中"}</dd></div>
                    <div><dt>总耗时</dt><dd>{formatMilliseconds(traceEnd - detail.startedAtUnixMs)}</dd></div>
                    <div><dt>Span</dt><dd>{detail.spans.length}{detail.truncated ? `，丢弃 ${detail.droppedSpanCount}` : ""}</dd></div>
                  </dl>
                </header>

                {detail.spans.length === 0 ? (
                  <EmptyState title="这条 Trace 没有 Span" description="该 Trace 创建时尚未记录调用点。" />
                ) : (
                  <div className="trace-timeline-scroll" tabIndex={0} aria-label="Span 树与时间轴">
                    <div className="trace-timeline" role="tree" aria-label="调用链 Span 树">
                      <div className="trace-timeline-ruler" aria-hidden="true">
                        <span>调用点</span>
                        <div>{[0, 25, 50, 75, 100].map((percent) => <i key={percent} style={{ left: `${percent}%` }}>{formatMilliseconds(traceDuration * percent / 100)}</i>)}</div>
                        <span>耗时</span>
                      </div>
                      {rows.map(({ span, depth, hasChildren }) => {
                        const spanEnd = span.endedAtUnixMs || clock;
                        const left = clampPercent((span.startedAtUnixMs - detail.startedAtUnixMs) / traceDuration * 100);
                        const width = Math.max(0.45, clampPercent((spanEnd - span.startedAtUnixMs) / traceDuration * 100));
                        const isSelected = span.spanId === selectedSpanID;
                        return (
                          <div
                            key={span.spanId}
                            className={`trace-span-row ${isSelected ? "selected" : ""}`}
                            role="treeitem"
                            tabIndex={0}
                            aria-level={depth + 1}
                            aria-expanded={hasChildren ? !collapsed.has(span.spanId) : undefined}
                            aria-selected={isSelected}
                            onClick={() => setSelectedSpanID(span.spanId)}
                            onKeyDown={(event) => {
                              if (event.key !== "Enter" && event.key !== " ") return;
                              event.preventDefault();
                              setSelectedSpanID(span.spanId);
                            }}
                          >
                            <div className="trace-span-tree" style={{ paddingLeft: `${12 + depth * 18}px` }}>
                              {hasChildren ? (
                                <button type="button" className="trace-collapse" aria-label={`${collapsed.has(span.spanId) ? "展开" : "折叠"}${span.operation}`} onClick={(event) => { event.stopPropagation(); toggleCollapsed(span.spanId); }}>
                                  {collapsed.has(span.spanId) ? <ChevronRightIcon /> : <ChevronDownIcon />}
                                </button>
                              ) : <span className="trace-collapse-spacer" />}
                              <span className={`trace-category-mark ${span.category}`} aria-hidden="true" />
                              <span><strong>{span.operation}</strong><small>{categoryLabel(span.category)}，+{formatMilliseconds(span.startedAtUnixMs - detail.startedAtUnixMs)}</small></span>
                            </div>
                            <div className="trace-span-track" aria-label={`${span.operation} 从 ${formatDateTimePrecise(span.startedAtUnixMs)} 到 ${span.endedAtUnixMs ? formatDateTimePrecise(span.endedAtUnixMs) : "进行中"}`}>
                              <span className={`trace-span-bar ${span.category} ${span.status}`} style={{ left: `${left}%`, width: `${Math.min(100 - left, width)}%` }} />
                            </div>
                            <div className="trace-span-duration">{span.endedAtUnixMs ? formatMilliseconds(span.durationMs) : formatMilliseconds(spanEnd - span.startedAtUnixMs)}</div>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                )}

                {selectedSpan ? <TraceSpanInspector span={selectedSpan} /> : null}
              </>
            ) : null}
          </div>
        </div>
      )}
    </section>
  );
}

function TraceSummaryButton({ trace, selected, onSelect }: { trace: MessageTrace; selected: boolean; onSelect: () => void }) {
  return (
    <button type="button" className={`trace-summary ${selected ? "selected" : ""}`} aria-pressed={selected} onClick={onSelect}>
      <span><code>{trace.traceId}</code><span className={`trace-status ${trace.status}`}>{statusLabel(trace.status)}</span></span>
      <strong>{trace.turnId ? `Turn ${shortID(trace.turnId)}` : "尚未创建 Turn"}</strong>
      <small>{formatDateTimePrecise(trace.receivedAtUnixMs)}<span>{trace.completedAtUnixMs ? formatMilliseconds(trace.totalDurationMs) : "进行中"}</span></small>
    </button>
  );
}

function TraceDetailLoading() {
  return <div className="trace-detail-loading" aria-label="正在读取调用链"><span /><span /><span /><span /></div>;
}

function TraceFailure({ title, description, onRetry }: { title: string; description: string; onRetry: () => void }) {
  return (
    <div className="trace-failure" role="alert">
      <strong>{title}</strong><span>{description}</span><Button variant="soft" onClick={onRetry}><ReloadIcon />重试</Button>
    </div>
  );
}

function TraceSpanInspector({ span }: { span: TraceSpan }) {
  const attributes = Object.entries(span.attributes);
  return (
    <section className="trace-span-inspector" aria-label="Span 详情">
      <header><div><strong>{span.operation}</strong><span>{categoryLabel(span.category)}</span></div><span className={`trace-status ${span.status}`}>{statusLabel(span.status)}</span></header>
      <dl className="trace-span-facts">
        <div><dt>开始时间</dt><dd>{formatDateTimePrecise(span.startedAtUnixMs)}</dd></div>
        <div><dt>结束时间</dt><dd>{span.endedAtUnixMs ? formatDateTimePrecise(span.endedAtUnixMs) : "进行中"}</dd></div>
        <div><dt>耗时</dt><dd>{span.endedAtUnixMs ? formatMilliseconds(span.durationMs) : "持续更新"}</dd></div>
        <div><dt>Span ID</dt><dd><code>{span.spanId}</code></dd></div>
        <div><dt>Parent Span</dt><dd>{span.parentSpanId ? <code>{span.parentSpanId}</code> : "根节点"}</dd></div>
      </dl>
      <div className="trace-span-attributes">
        <strong>安全属性</strong>
        {attributes.length === 0 ? <span>无附加属性</span> : <dl>{attributes.map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl>}
      </div>
    </section>
  );
}

function buildTraceRows(spans: TraceSpan[], collapsed: Set<string>): TraceRow[] {
  const children = new Map<string, TraceSpan[]>();
  const roots: TraceSpan[] = [];
  for (const span of spans) {
    if (!span.parentSpanId) roots.push(span);
    else children.set(span.parentSpanId, [...(children.get(span.parentSpanId) ?? []), span]);
  }
  const rows: TraceRow[] = [];
  const visit = (span: TraceSpan, depth: number) => {
    const nested = children.get(span.spanId) ?? [];
    rows.push({ span, depth, hasChildren: nested.length > 0 });
    if (!collapsed.has(span.spanId)) nested.forEach((child) => visit(child, depth + 1));
  };
  roots.forEach((span) => visit(span, 0));
  return rows;
}

export function UsageDashboard({ report }: { report: UsageReport }) {
  const [laneFilter, setLaneFilter] = useState<UsageLaneFilter>(USAGE_LANE_FILTER_ALL);
  const total = aggregateUsage(report.overall, laneFilter);
  const visibleTurns = report.turns.filter((turn) => turnMatchesLane(turn, laneFilter));
  const hasUsage = report.overall.length > 0;

  return (
    <section className="observability-section usage-dashboard">
      <SectionHeading
        title="累计模型用量"
        description="缓存命中率只计算模型提供商明确报告了缓存观测的输入。"
        aside={
          <div className="segmented-control" role="group" aria-label="调用阶段筛选">
            <button type="button" className={laneFilter === USAGE_LANE_FILTER_ALL ? "active" : ""} aria-pressed={laneFilter === USAGE_LANE_FILTER_ALL} onClick={() => setLaneFilter(USAGE_LANE_FILTER_ALL)}>全部</button>
            <button type="button" className={laneFilter === USAGE_LANE_FILTER_RESPOND ? "active" : ""} aria-pressed={laneFilter === USAGE_LANE_FILTER_RESPOND} onClick={() => setLaneFilter(USAGE_LANE_FILTER_RESPOND)}>仅回复</button>
          </div>
        }
      />

      {hasUsage ? (
        <div className="usage-summary" aria-label="累计 Token 指标">
          <UsageMetric label="缓存命中" value={formatTokenCount(total.cachedInputTokens)} detail={`${formatTokenCount(total.callCount)} 次模型调用`} testId="usage-cached" />
          <UsageMetric label="未命中输入" value={formatTokenCount(total.uncachedInputTokens)} detail={`${formatTokenCount(total.inputTokens)} 输入 Token`} testId="usage-uncached" />
          <UsageMetric label="输出" value={formatTokenCount(total.outputTokens)} detail={`${formatTokenCount(total.cacheWriteTokens)} 个缓存写入`} testId="usage-output" />
          <UsageMetric label="缓存命中率" value={formatHitRate(usageHitRate(total))} detail={`${formatTokenCount(total.cachedObservedInputTokens)} 个已观测输入`} testId="usage-hit-rate" />
        </div>
      ) : (
        <EmptyState title="还没有模型用量" description="完成一次模型调用后，这里会显示 Token 与缓存利用。" />
      )}

      <section className="usage-recent">
        <div className="observability-subheading">
          <div><strong>最近会话回合</strong><span>{visibleTurns.length} 条可见，共 {formatTokenCount(report.turnCount)} 次发送</span></div>
          {report.truncated ? <span className="usage-truncated">仅展示最近记录，累计仍覆盖全部历史</span> : null}
        </div>
        {visibleTurns.length === 0 ? (
          <EmptyState title="当前筛选下没有发送记录" />
        ) : (
          <div className="table-scroll">
            <table className="data-table usage-table">
              <thead><tr><th>时间</th><th>Turn</th><th>角色</th><th>状态</th><th>输入</th><th>缓存命中</th><th>未命中</th><th>输出</th><th>命中率</th></tr></thead>
              <tbody>{visibleTurns.map((turn) => {
                const usage = aggregateUsage(turn.lanes, laneFilter);
                return (
                  <tr key={turn.turnId} data-testid={`usage-turn-${turn.turnId}`}>
                    <td>{formatUsageTime(turn.createdAtUnixMs)}</td><td><code>{turn.turnId}</code></td><td><code>{turn.characterId ? turn.characterId.slice(0, 8) : "无"}</code></td>
                    <td><span className={`usage-status ${turn.status}`}>{statusLabel(turn.status)}</span></td><td>{formatTokenCount(usage.inputTokens)}</td><td>{formatTokenCount(usage.cachedInputTokens)}</td>
                    <td>{formatTokenCount(usage.uncachedInputTokens)}</td><td>{formatTokenCount(usage.outputTokens)}</td><td>{formatHitRate(usageHitRate(usage))}</td>
                  </tr>
                );
              })}</tbody>
            </table>
          </div>
        )}
      </section>
    </section>
  );
}

function UsageMetric({ label, value, detail, testId }: { label: string; value: string; detail: string; testId: string }) {
  return <div className="usage-metric" data-testid={testId}><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>;
}

function LiveLogPanel({ token }: { token: string }) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [pending, setPending] = useState<LogEntry[]>([]);
  const [droppedOnClient, setDroppedOnClient] = useState(0);
  const [level, setLevel] = useState<LogLevel>("info");
  const [loggerPrefix, setLoggerPrefix] = useState("");
  const [streamStatus, setStreamStatus] = useState<StreamStatus>("connecting");
  const [streamError, setStreamError] = useState("");
  const [streamRevision, setStreamRevision] = useState(0);
  const pausedRef = useRef(false);

  useEffect(() => {
    const controller = new AbortController();
    setLogs([]);
    setPending([]);
    setDroppedOnClient(0);
    setStreamError("");
    setStreamStatus("connecting");
    void followLogs({
      level,
      loggerPrefix,
      signal: controller.signal,
      onReady: () => setStreamStatus(pausedRef.current ? "paused" : "live"),
      onEntry: (entry) => {
        if (pausedRef.current) {
          setPending((current) => {
            const result = appendPendingLogs(current, [entry]);
            if (result.dropped > 0) setDroppedOnClient((count) => count + result.dropped);
            return result.entries;
          });
          return;
        }
        setLogs((current) => {
          const result = mergeVisibleLogs(current, [entry]);
          if (result.dropped > 0) setDroppedOnClient((count) => count + result.dropped);
          return result.entries;
        });
      },
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      setStreamError(errorMessage(error));
      setStreamStatus("disconnected");
    });
    return () => controller.abort();
  }, [level, loggerPrefix, streamRevision, token]);

  function pause() {
    pausedRef.current = true;
    setStreamStatus("paused");
  }

  function resume() {
    const merged = mergeVisibleLogs(logs, pending);
    setLogs(merged.entries);
    setDroppedOnClient((count) => count + merged.dropped);
    setPending([]);
    pausedRef.current = false;
    setStreamStatus(streamError ? "disconnected" : "live");
  }

  return (
    <section className="observability-section log-tool">
      <SectionHeading title="实时日志" description="按级别和记录器筛选当前 Core 日志流。" />
      <div className="log-toolbar">
        <div className="log-filter">
          <Text as="label" size="1" color="gray">最低级别</Text>
          <Select.Root value={level} onValueChange={(value) => setLevel(value as LogLevel)}>
            <Select.Trigger aria-label="最低日志级别" />
            <Select.Content position="popper" sideOffset={6}>
              <Select.Item value="debug">调试</Select.Item><Select.Item value="info">信息</Select.Item><Select.Item value="warn">警告</Select.Item><Select.Item value="error">错误</Select.Item>
            </Select.Content>
          </Select.Root>
        </div>
        <div className="log-filter logger-filter">
          <Text as="label" size="1" color="gray">记录器前缀</Text>
          <TextField.Root value={loggerPrefix} onChange={(event) => setLoggerPrefix(event.target.value)} placeholder="全部记录器" aria-label="记录器前缀" />
        </div>
        <div className="log-stream-actions">
          <span className={`stream-state ${streamStatus}`}>{streamStatusLabel(streamStatus)}</span>
          {streamStatus === "paused" ? <Button variant="soft" onClick={resume}><PlayIcon />继续</Button> : <Button variant="soft" disabled={streamStatus !== "live"} onClick={pause}><PauseIcon />暂停</Button>}
          {streamStatus === "disconnected" || streamStatus === "error" ? <Button onClick={() => setStreamRevision((value) => value + 1)}><ReloadIcon />重连</Button> : null}
        </div>
      </div>
      <div className="log-meta">
        <span>{logs.length} 条可见</span>{pending.length > 0 ? <span>{pending.length} 条暂停缓冲</span> : null}{droppedOnClient > 0 ? <span>{droppedOnClient} 条客户端丢弃</span> : null}
        {streamError ? <span className="log-stream-error">{streamError}</span> : null}
      </div>
      <div className="log-list" role="log" aria-live="off">
        {logs.length === 0 ? <div className="log-empty">{streamStatus === "connecting" ? "正在连接日志流" : "暂无匹配日志"}</div> : logs.map((entry) => <LogRow key={entry.sequence} entry={entry} />)}
      </div>
    </section>
  );
}

function LogRow({ entry }: { entry: LogEntry }) {
  return (
    <article className="log-row">
      <time dateTime={new Date(entry.timestampUnixMs).toISOString()}>{formatTime(entry.timestampUnixMs)}</time>
      <span className={`log-level ${entry.level}`}>{logLevelLabel(entry.level)}</span>
      <span className="log-logger">{entry.logger || "root"}</span>
      <div className="log-content">
        <div className="log-message">{entry.message}</div>
        {entry.fields.length > 0 ? <details><summary>{entry.fields.length} 个字段</summary><dl>{entry.fields.map((field, index) => <div key={`${field.key}-${index}`}><dt>{field.key}</dt><dd>{field.value}</dd></div>)}</dl></details> : null}
      </div>
    </article>
  );
}

function statusLabel(status: string) {
  return ({ completed: "已完成", failed: "失败", interrupted: "已中断", silent: "静默", active: "进行中", running: "进行中" } as Record<string, string>)[status] ?? status;
}
function sourceLabel(source: string) { return ({ direct: "直接消息", ambient: "环境消息" } as Record<string, string>)[source] ?? source; }
function logLevelLabel(level: LogLevel) { return { debug: "调试", info: "信息", warn: "警告", error: "错误" }[level]; }
function formatNumber(value: number) { return new Intl.NumberFormat("zh-CN").format(value); }
function formatTrendValue(value: number, decimals = 0) {
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: decimals, minimumFractionDigits: decimals }).format(value);
}
function formatAxisValue(value: number) {
  if (value >= 1_000_000) return `${formatTrendValue(value / 1_000_000, 1)}m`;
  if (value >= 1_000) return `${formatTrendValue(value / 1_000, 1)}k`;
  if (value >= 10) return formatTrendValue(Math.round(value));
  return formatTrendValue(value, value % 1 === 0 ? 0 : 1);
}
function formatTime(timestamp: number) { return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(timestamp); }
function formatDateTime(timestamp: number) { return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(timestamp); }
function formatDateTimePrecise(timestamp: number) {
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    fractionalSecondDigits: 3,
    hour12: false,
  }).format(timestamp);
}
function formatMilliseconds(value: number) {
  const milliseconds = Math.max(0, Math.round(value));
  if (milliseconds < 1000) return `${milliseconds} ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(milliseconds < 10_000 ? 2 : 1)} s`;
  const minutes = Math.floor(milliseconds / 60_000);
  const seconds = Math.round((milliseconds % 60_000) / 1000);
  return `${minutes} 分 ${seconds} 秒`;
}
function shortID(value: string) { return value.length > 12 ? value.slice(0, 8) : value; }
function clampPercent(value: number) { return Math.max(0, Math.min(100, value)); }
function categoryLabel(category: string) {
  return ({
    message: "消息",
    participation: "参与判断",
    turn: "Turn",
    lifecycle: "生命周期",
    context: "上下文",
    model: "模型",
    tool: "工具",
    compile: "回复编译",
    delivery: "回复交付",
  } as Record<string, string>)[category] ?? category;
}
function errorMessage(error: unknown) { return error instanceof Error ? error.message : String(error); }
function streamStatusLabel(status: StreamStatus) { return { connecting: "连接中", live: "实时", paused: "已暂停", disconnected: "已断开", error: "错误" }[status]; }
