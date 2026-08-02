import { Button, Select, Switch, Text, TextArea, TextField } from "@radix-ui/themes";
import { ReloadIcon } from "@radix-ui/react-icons";
import { useEffect, useState } from "react";
import { api } from "../api";
import { Field, PageHeader } from "../components/ui";
import {
  USAGE_LANE_FILTER_ALL,
  USAGE_LANE_FILTER_RESPOND,
  aggregateUsage,
  formatHitRate,
  formatTokenCount,
  formatUsageTime,
  parseUsageReport,
  turnMatchesLane,
  usageHitRate,
  type UsageLaneFilter,
  type UsageReport,
} from "../usageReport";

export function OverviewPage({ onToast }: { onToast: (m: string, e?: boolean) => void }) {
  const [status, setStatus] = useState<any>(null);
  const [characters, setCharacters] = useState<any>(null);

  async function reload() {
    const [s, c] = await Promise.all([api("/status"), api("/characters")]);
    setStatus(s);
    setCharacters(c);
  }

  useEffect(() => {
    reload().catch((e) => onToast(e.message, true));
  }, []);

  const model = status?.model || {};
  const speech = status?.speech || {};
  const web = status?.webSearch || {};
  const semantic = status?.semanticEmbedding || {};
  const active = characters?.active;

  const cards = [
    {
      title: "激活角色",
      ready: Boolean(active),
      value: active?.name || "尚未激活",
      detail: active ? active.characterId : "在角色页创建并激活",
    },
    {
      title: "模型",
      ready: Boolean(model.configured),
      value: model.configured ? model.model || "已连接" : "尚未配置",
      detail: model.endpoint || "保存 Endpoint 与模型后即可对话",
    },
    {
      title: "语音 TTS",
      ready: Boolean(speech.configured && speech.enabled),
      value: speech.enabled ? "已启用" : speech.configured ? "已配置未启用" : "尚未配置",
      detail: speech.defaultSpeaker || speech.synthesisModel || "火山语音克隆 HTTP",
    },
    {
      title: "公开检索",
      ready: Boolean(web.enabled),
      value: web.enabled ? "已开启" : "已关闭",
      detail: web.baseUrl || "默认 OpenSERP",
    },
    {
      title: "语义嵌入",
      ready: Boolean(semantic.configured && semantic.enabled),
      value: semantic.provider && semantic.provider !== "none" ? semantic.provider : "FTS-only",
      detail: semantic.model || "默认关键词检索",
    },
  ];

  return (
    <section>
      <PageHeader
        title="运行概览"
        description="一眼看清角色、模型、语音与检索是否就绪。"
        action={
          <Button variant="soft" onClick={() => void reload().then(() => onToast("已刷新")).catch((e) => onToast(e.message, true))}>
            刷新
          </Button>
        }
      />
      <div className="grid-stats">
        {cards.map((card) => (
          <article key={card.title} className="card stat-card">
            <div className="label">{card.title}</div>
            <div className="value">{card.value}</div>
            <div className="detail">{card.detail}</div>
            <Text size="1" color={card.ready ? "teal" : "gray"} mt="2">
              {card.ready ? "已就绪" : "待配置"}
            </Text>
          </article>
        ))}
      </div>
    </section>
  );
}

type MemoryRecord = {
  id: string;
  kind: string;
  content: string;
  scope: { type: string; characterId?: string };
};

export function IntelligencePage({ onToast }: { onToast: (m: string, e?: boolean) => void }) {
  const [intel, setIntel] = useState<any>(null);
  const [memories, setMemories] = useState<{ global: MemoryRecord[]; character: MemoryRecord[]; needsReview: MemoryRecord[] } | null>(null);
  const [webEnabled, setWebEnabled] = useState(false);
  const [webBase, setWebBase] = useState("");
  const [kind, setKind] = useState("preference");
  const [content, setContent] = useState("");
  const [memoryTab, setMemoryTab] = useState<"global" | "character" | "needsReview">("global");

  async function reload() {
    const i = await api<any>("/intelligence");
    setIntel(i);
    setWebEnabled(Boolean(i.webSearch?.enabled));
    setWebBase(i.webSearch?.baseUrl || "");
    const m = await api<any>("/memories/personal");
    setMemories(m);
  }

  useEffect(() => {
    reload().catch((e) => onToast(e.message, true));
  }, []);

  const summary = intel?.summary || {};

  return (
    <section>
      <PageHeader
        title="智能层"
        description="会话、个人记忆和知识都在本机。公开资料可走检索。"
        status={intel?.ready ? "本地层已就绪" : "本地层不可用"}
        ready={Boolean(intel?.ready)}
      />
      <div className="grid-stats" style={{ marginBottom: 16 }}>
        {[
          ["全局记忆", summary.activeGlobalMemories],
          ["角色关系", summary.activeCharacterMemories],
          ["待审记忆", summary.needsReviewMemories],
          ["后台任务", intel?.activeBackgroundJobs ?? 0],
        ].map(([label, value]) => (
          <div key={String(label)} className="card stat-card">
            <div className="label">{label}</div>
            <div className="value">{value ?? "—"}</div>
          </div>
        ))}
      </div>

      <div className="stack">
        <div className="card">
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 12 }}>
            <div>
              <Text weight="medium">允许检索公开资料</Text>
              <Text as="p" size="1" color="gray">
                Compose 内默认走 OpenSERP sidecar。
              </Text>
            </div>
            <Switch
              checked={webEnabled}
              onCheckedChange={(checked) => {
                setWebEnabled(checked);
                void api("/config/web-search", {
                  method: "PUT",
                  body: JSON.stringify({ enabled: checked, baseUrl: webBase }),
                })
                  .then(() => onToast("检索设置已保存"))
                  .catch((e) => onToast(e.message, true));
              }}
            />
          </div>
          <Field label="Base URL">
            <TextField.Root
              value={webBase}
              onChange={(e) => setWebBase(e.target.value)}
              placeholder="http://openserp:7000"
            />
          </Field>
          <div className="form-actions">
            <Button
              onClick={() =>
                void api("/config/web-search", {
                  method: "PUT",
                  body: JSON.stringify({ enabled: webEnabled, baseUrl: webBase }),
                })
                  .then(() => onToast("检索设置已保存"))
                  .catch((e) => onToast(e.message, true))
              }
            >
              保存检索
            </Button>
          </div>
        </div>

        <div className="card memory-ledger">
          <Text weight="medium">个人记忆台账</Text>
          <Text as="p" size="1" color="gray" mb="3">
            手动写入需要已有会话回合；无对话时列表仍可浏览。
          </Text>
          <div className="memory-compose">
            <Field label="类型">
              <Select.Root value={kind} onValueChange={setKind}>
                <Select.Trigger />
                <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
                  <Select.Item value="preference">偏好</Select.Item>
                  <Select.Item value="profile">用户资料</Select.Item>
                  <Select.Item value="experience">经历</Select.Item>
                  <Select.Item value="relationship">当前角色关系</Select.Item>
                </Select.Content>
              </Select.Root>
            </Field>
            <Field label="内容">
              <TextArea value={content} onChange={(e) => setContent(e.target.value)} rows={2} />
            </Field>
            <div className="form-actions">
              <Button
                onClick={() =>
                  void (async () => {
                    const isRelationship = kind === "relationship";
                    let scope: { type: string; characterId?: string } = { type: "global" };
                    if (isRelationship) {
                      const catalog = await api<{ active?: { characterId: string } }>("/characters");
                      if (!catalog.active?.characterId) {
                        throw new Error("写入角色关系前请先激活角色");
                      }
                      scope = { type: "character", characterId: catalog.active.characterId };
                    }
                    await api("/memories/personal", {
                      method: "POST",
                      body: JSON.stringify({ kind, scope, content }),
                    });
                    setContent("");
                    onToast(isRelationship ? "角色关系记忆已写入" : "全局记忆已写入");
                    await reload();
                  })().catch((e) => onToast(e.message, true))
                }
              >
                {kind === "relationship" ? "写入角色关系" : "写入全局记忆"}
              </Button>
            </div>
          </div>

          <div className="memory-buckets" role="tablist" aria-label="记忆分类">
            {(
              [
                { id: "global", label: "全局" },
                { id: "character", label: "角色关系" },
                { id: "needsReview", label: "待审" },
              ] as const
            ).map((tab) => (
              <button
                key={tab.id}
                type="button"
                role="tab"
                className={`memory-tab ${memoryTab === tab.id ? "active" : ""}`}
                aria-selected={memoryTab === tab.id}
                onClick={() => setMemoryTab(tab.id)}
              >
                {tab.label}
                <span className="memory-tab-count">{(memories?.[tab.id] || []).length}</span>
              </button>
            ))}
          </div>
          <div className="memory-panel">
            {(memories?.[memoryTab] || []).length === 0 ? (
              <Text as="p" size="2" color="gray" className="memory-empty">
                暂无条目
              </Text>
            ) : (
              (memories?.[memoryTab] || []).map((m) => (
                <div key={m.id} className="memory-row">
                  <div>
                    <Text size="2">{m.content}</Text>
                    <Text as="p" size="1" color="gray">
                      {m.kind}
                    </Text>
                  </div>
                  <Button
                    size="1"
                    color="tomato"
                    variant="soft"
                    onClick={() =>
                      void api(`/memories/personal/${m.id}`, { method: "DELETE" })
                        .then(() => {
                          onToast("已删除");
                          return reload();
                        })
                        .catch((e) => onToast(e.message, true))
                    }
                  >
                    删除
                  </Button>
                </div>
              ))
            )}
          </div>
        </div>
      </div>
    </section>
  );
}

export function UsagePage({ onToast }: { onToast: (m: string, e?: boolean) => void }) {
  const [report, setReport] = useState<UsageReport | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const [laneFilter, setLaneFilter] = useState<UsageLaneFilter>(USAGE_LANE_FILTER_ALL);

  useEffect(() => {
    let active = true;
    setReport(null);
    setError("");
    setLoading(true);
    api<unknown>("/usage")
      .then((value) => {
        if (active) setReport(parseUsageReport(value));
      })
      .catch((cause: unknown) => {
        if (!active) return;
        const message = cause instanceof Error ? cause.message : String(cause);
        setError(message);
        onToast(message, true);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [revision]);

  const total = report ? aggregateUsage(report.overall, laneFilter) : null;
  const visibleTurns = report?.turns.filter((turn) => turnMatchesLane(turn, laneFilter)) ?? [];
  const hasUsage = Boolean(report && report.overall.length > 0);

  return (
    <section className="usage-page">
      <PageHeader
        title="用量"
        description="查看模型输入、缓存利用和每次发送的 Token 构成。"
        status={loading ? "读取中" : error ? "数据不可用" : report ? `${formatTokenCount(report.turnCount)} 次发送` : undefined}
        ready={Boolean(report)}
        action={
          <Button variant="soft" disabled={loading} onClick={() => setRevision((value) => value + 1)}>
            <ReloadIcon /> 刷新
          </Button>
        }
      />

      {error ? (
        <div className="usage-error" role="alert">
          <strong>用量数据不可用</strong>
          <span>{error}</span>
        </div>
      ) : null}

      {report ? (
        <>
          <div className="usage-toolbar">
            <div>
              <h2>累计用量</h2>
              <p>累计覆盖全部历史；缓存命中率只计算 provider 已报告观测的输入。</p>
            </div>
            <div className="usage-mode" role="group" aria-label="Lane 筛选">
              <button
                type="button"
                className={laneFilter === USAGE_LANE_FILTER_ALL ? "active" : ""}
                aria-pressed={laneFilter === USAGE_LANE_FILTER_ALL}
                onClick={() => setLaneFilter(USAGE_LANE_FILTER_ALL)}
              >
                全部
              </button>
              <button
                type="button"
                className={laneFilter === USAGE_LANE_FILTER_RESPOND ? "active" : ""}
                aria-pressed={laneFilter === USAGE_LANE_FILTER_RESPOND}
                onClick={() => setLaneFilter(USAGE_LANE_FILTER_RESPOND)}
              >
                仅 respond
              </button>
            </div>
          </div>

          {hasUsage && total ? (
            <div className="usage-summary" aria-label="累计 Token 指标">
              <UsageMetric label="缓存命中" value={formatTokenCount(total.cachedInputTokens)} detail={`${formatTokenCount(total.callCount)} 次模型调用`} testId="usage-cached" />
              <UsageMetric label="未命中输入" value={formatTokenCount(total.uncachedInputTokens)} detail={`${formatTokenCount(total.inputTokens)} 输入 Token`} testId="usage-uncached" />
              <UsageMetric label="输出" value={formatTokenCount(total.outputTokens)} detail={`${formatTokenCount(total.cacheWriteTokens)} cache write`} testId="usage-output" />
              <UsageMetric label="缓存命中率" value={formatHitRate(usageHitRate(total))} detail={`${formatTokenCount(total.cachedObservedInputTokens)} 已观测输入`} testId="usage-hit-rate" />
            </div>
          ) : (
            <div className="usage-empty">还没有可统计的模型用量。</div>
          )}

          <div className="usage-recent">
            <div className="usage-recent-heading">
              <div>
                <h2>最近发送</h2>
                <p>{visibleTurns.length} 条可见 · 全部历史 {formatTokenCount(report.turnCount)} 次</p>
              </div>
              {report.truncated ? <span className="usage-truncated">仅展示最近记录，累计仍覆盖全部历史</span> : null}
            </div>
            {visibleTurns.length === 0 ? (
              <div className="usage-empty">当前筛选下没有发送记录。</div>
            ) : (
              <div className="usage-table-wrap">
                <table className="usage-table">
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>Turn</th>
                      <th>角色</th>
                      <th>状态</th>
                      <th>输入</th>
                      <th>缓存命中</th>
                      <th>未命中</th>
                      <th>输出</th>
                      <th>命中率</th>
                    </tr>
                  </thead>
                  <tbody>
                    {visibleTurns.map((turn) => {
                      const usage = aggregateUsage(turn.lanes, laneFilter);
                      return (
                        <tr key={turn.turnId} data-testid={`usage-turn-${turn.turnId}`}>
                          <td><time dateTime={new Date(turn.createdAtUnixMs).toISOString()}>{formatUsageTime(turn.createdAtUnixMs)}</time></td>
                          <td><code>{turn.turnId.slice(0, 8)}</code></td>
                          <td><code>{turn.characterId ? turn.characterId.slice(0, 8) : "—"}</code></td>
                          <td><span className={`usage-status ${turn.status}`}>{turn.status}</span></td>
                          <td>{formatTokenCount(usage.inputTokens)}</td>
                          <td>{formatTokenCount(usage.cachedInputTokens)}</td>
                          <td>{formatTokenCount(usage.uncachedInputTokens)}</td>
                          <td>{formatTokenCount(usage.outputTokens)}</td>
                          <td>{formatHitRate(usageHitRate(usage))}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      ) : null}
    </section>
  );
}

function UsageMetric({ label, value, detail, testId }: { label: string; value: string; detail: string; testId: string }) {
  return (
    <div className="usage-metric">
      <span>{label}</span>
      <strong data-testid={testId}>{value}</strong>
      <small>{detail}</small>
    </div>
  );
}
