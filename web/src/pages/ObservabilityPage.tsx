import { PauseIcon, PlayIcon, ReloadIcon } from "@radix-ui/react-icons";
import { Button, Select, Text, TextField } from "@radix-ui/themes";
import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import { PageHeader } from "../components/ui";
import {
  appendPendingLogs,
  followLogs,
  mergeVisibleLogs,
  parseMetrics,
  type LogEntry,
  type LogLevel,
  type MetricsSnapshot,
} from "../observability";

type StreamStatus = "connecting" | "live" | "paused" | "disconnected" | "error";

export function ObservabilityPage({ token }: { token: string }) {
  const [metrics, setMetrics] = useState<MetricsSnapshot | null>(null);
  const [metricsError, setMetricsError] = useState("");
  const [metricsRevision, setMetricsRevision] = useState(0);
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
    let active = true;
    setMetrics(null);
    setMetricsError("");
    api<unknown>("/metrics")
      .then((value) => {
        if (active) setMetrics(parseMetrics(value));
      })
      .catch((error: unknown) => {
        if (active) setMetricsError(errorMessage(error));
      });
    return () => {
      active = false;
    };
  }, [metricsRevision, token]);

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

  const totals = metrics ? usageTotals(metrics) : null;
  const cacheRate = totals && totals.cachedObservedInputTokens > 0
    ? `${Math.round((totals.cachedInputTokens / totals.cachedObservedInputTokens) * 100)}%`
    : "N/A";

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
    <section className="observability-page">
      <PageHeader
        title="日志与指标"
        description="当前 Core 进程的脱敏日志、请求与运行观测。"
        status={metricsError ? "指标不可用" : metrics ? "指标已更新" : "读取指标"}
        ready={Boolean(metrics)}
        action={
          <Button variant="soft" onClick={() => setMetricsRevision((value) => value + 1)}>
            <ReloadIcon /> 刷新
          </Button>
        }
      />

      {metricsError ? <div className="observability-error">{metricsError}</div> : null}
      {metrics ? (
        <div className="observability-metrics" aria-label="运行指标摘要">
          <Metric label="进程" value={formatDuration(metrics.process.uptimeSeconds)} detail={`${metrics.process.goroutines} goroutines`} />
          <Metric label="HTTP" value={formatNumber(metrics.http.total)} detail={`${metrics.http.status4xx + metrics.http.status5xx} errors · ${metrics.http.inFlight} active`} />
          <Metric label="Turn" value={formatNumber(metrics.usage.turnCount)} detail={`${formatNumber(totals?.inputTokens ?? 0)} in · ${formatNumber(totals?.outputTokens ?? 0)} out`} />
          <Metric label="Cache" value={cacheRate} detail={`${formatNumber(totals?.cachedInputTokens ?? 0)} cached tokens`} />
          <Metric label="后台任务" value={formatNumber(metrics.runtime.activeBackgroundJobs)} detail={`${metrics.runtime.eventSubscribers} event subscribers`} />
          <Metric label="日志" value={formatNumber(metrics.logs.retainedEntries)} detail={`${metrics.logs.droppedEntries} dropped · ${metrics.logs.activeSubscribers} live`} />
        </div>
      ) : null}

      <div className="log-tool">
        <div className="log-toolbar">
          <div className="log-filter">
            <Text as="label" size="1" color="gray">最低级别</Text>
            <Select.Root value={level} onValueChange={(value) => setLevel(value as LogLevel)}>
              <Select.Trigger aria-label="最低日志级别" />
              <Select.Content position="popper" sideOffset={6}>
                <Select.Item value="debug">Debug</Select.Item>
                <Select.Item value="info">Info</Select.Item>
                <Select.Item value="warn">Warn</Select.Item>
                <Select.Item value="error">Error</Select.Item>
              </Select.Content>
            </Select.Root>
          </div>
          <div className="log-filter logger-filter">
            <Text as="label" size="1" color="gray">Logger 前缀</Text>
            <TextField.Root
              value={loggerPrefix}
              onChange={(event) => setLoggerPrefix(event.target.value)}
              placeholder="全部 logger"
              aria-label="Logger 前缀"
            />
          </div>
          <div className="log-stream-actions">
            <span className={`stream-state ${streamStatus}`}>{streamStatusLabel(streamStatus)}</span>
            {streamStatus === "paused" ? (
              <Button variant="soft" onClick={resume}><PlayIcon />继续</Button>
            ) : (
              <Button variant="soft" disabled={streamStatus !== "live"} onClick={pause}><PauseIcon />暂停</Button>
            )}
            {streamStatus === "disconnected" || streamStatus === "error" ? (
              <Button onClick={() => setStreamRevision((value) => value + 1)}><ReloadIcon />重连</Button>
            ) : null}
          </div>
        </div>

        <div className="log-meta">
          <span>{logs.length} 条可见</span>
          {pending.length > 0 ? <span>{pending.length} 条暂停缓冲</span> : null}
          {droppedOnClient > 0 ? <span>{droppedOnClient} 条客户端丢弃</span> : null}
          {streamError ? <span className="log-stream-error">{streamError}</span> : null}
        </div>

        <div className="log-list" role="log" aria-live="off">
          {logs.length === 0 ? (
            <div className="log-empty">{streamStatus === "connecting" ? "正在连接日志流" : "暂无匹配日志"}</div>
          ) : logs.map((entry) => <LogRow key={entry.sequence} entry={entry} />)}
        </div>
      </div>
    </section>
  );
}

function Metric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return <div className="observability-metric"><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>;
}

function LogRow({ entry }: { entry: LogEntry }) {
  return (
    <article className="log-row">
      <time dateTime={new Date(entry.timestampUnixMs).toISOString()}>{formatTime(entry.timestampUnixMs)}</time>
      <span className={`log-level ${entry.level}`}>{entry.level}</span>
      <span className="log-logger">{entry.logger || "root"}</span>
      <div className="log-content">
        <div className="log-message">{entry.message}</div>
        {entry.fields.length > 0 ? (
          <details>
            <summary>{entry.fields.length} fields</summary>
            <dl>
              {entry.fields.map((field, index) => (
                <div key={`${field.key}-${index}`}><dt>{field.key}</dt><dd>{field.value}</dd></div>
              ))}
            </dl>
          </details>
        ) : null}
      </div>
    </article>
  );
}

function usageTotals(metrics: MetricsSnapshot) {
  return metrics.usage.overall.reduce((total, lane) => ({
    inputTokens: total.inputTokens + lane.inputTokens,
    outputTokens: total.outputTokens + lane.outputTokens,
    cachedInputTokens: total.cachedInputTokens + lane.cachedInputTokens,
    cachedObservedInputTokens: total.cachedObservedInputTokens + lane.cachedObservedInputTokens,
  }), { inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, cachedObservedInputTokens: 0 });
}

function formatNumber(value: number) { return new Intl.NumberFormat("zh-CN").format(value); }
function formatDuration(seconds: number) { return seconds < 60 ? `${seconds}s` : seconds < 3600 ? `${Math.floor(seconds / 60)}m` : `${Math.floor(seconds / 3600)}h`; }
function formatTime(timestamp: number) { return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(timestamp); }
function errorMessage(error: unknown) { return error instanceof Error ? error.message : String(error); }
function streamStatusLabel(status: StreamStatus) {
  return { connecting: "连接中", live: "实时", paused: "已暂停", disconnected: "已断开", error: "错误" }[status];
}
