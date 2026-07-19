import { Button, Select, Switch, Text, TextArea, TextField } from "@radix-ui/themes";
import { useEffect, useState } from "react";
import { api } from "../api";
import { Field, PageHeader } from "../components/ui";

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
  const [provider, setProvider] = useState("none");
  const [semEnabled, setSemEnabled] = useState(false);
  const [semModel, setSemModel] = useState("");
  const [kind, setKind] = useState("preference");
  const [content, setContent] = useState("");
  const [memoryTab, setMemoryTab] = useState<"global" | "character" | "needsReview">("global");

  async function reload() {
    const i = await api<any>("/intelligence");
    setIntel(i);
    setWebEnabled(Boolean(i.webSearch?.enabled));
    setWebBase(i.webSearch?.baseUrl || "");
    setProvider(i.semanticEmbedding?.provider || "none");
    setSemEnabled(Boolean(i.semanticEmbedding?.enabled));
    setSemModel(i.semanticEmbedding?.model || "");
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

        <div className="card">
          <Text weight="medium">语义嵌入</Text>
          <Field label="Provider">
            <Select.Root value={provider} onValueChange={setProvider}>
              <Select.Trigger />
              <Select.Content position="popper" side="bottom" align="start" sideOffset={6}>
                <Select.Item value="none">none（FTS-only）</Select.Item>
                <Select.Item value="openai_compatible_api">openai_compatible_api</Select.Item>
              </Select.Content>
            </Select.Root>
          </Field>
          <label style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
            <Switch checked={semEnabled} onCheckedChange={setSemEnabled} />
            <Text size="2">启用语义检索</Text>
          </label>
          <Field label="Embedding model">
            <TextField.Root value={semModel} onChange={(e) => setSemModel(e.target.value)} />
          </Field>
          <div className="form-actions">
            <Button
              onClick={() =>
                void api("/config/semantic-embedding", {
                  method: "PUT",
                  body: JSON.stringify({
                    schema_version: 1,
                    provider,
                    enabled: semEnabled,
                    model: semModel,
                    dimensions: 512,
                  }),
                })
                  .then(() => onToast("语义嵌入已保存"))
                  .catch((e) => onToast(e.message, true))
              }
            >
              保存语义
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
  const [report, setReport] = useState<any>(null);

  useEffect(() => {
    api("/usage")
      .then(setReport)
      .catch((e) => onToast(e.message, true));
  }, []);

  const overall = report?.overall || [];
  const turns = report?.turns || [];

  return (
    <section>
      <PageHeader
        title="用量"
        description="按 lane 汇总的 token 用量；来自本机会话账本。"
        status={report ? `${report.turnCount ?? 0} 次发送` : "读取中"}
        ready={Boolean(report)}
      />
      <div className="card" style={{ marginBottom: 16 }}>
        <Text weight="medium" mb="2">
          总体
        </Text>
        <table className="usage-table">
          <thead>
            <tr>
              <th>Lane</th>
              <th>Input</th>
              <th>Output</th>
              <th>Cached</th>
              <th>Calls</th>
            </tr>
          </thead>
          <tbody>
            {overall.length === 0 ? (
              <tr>
                <td colSpan={5}>暂无用量</td>
              </tr>
            ) : (
              overall.map((row: any) => (
                <tr key={row.lane}>
                  <td>{row.lane}</td>
                  <td>{row.inputTokens}</td>
                  <td>{row.outputTokens}</td>
                  <td>{row.cachedInputTokens}</td>
                  <td>{row.callCount}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
      <div className="card">
        <Text weight="medium" mb="2">
          最近回合
        </Text>
        <table className="usage-table">
          <thead>
            <tr>
              <th>Turn</th>
              <th>Status</th>
              <th>Character</th>
              <th>Lanes</th>
            </tr>
          </thead>
          <tbody>
            {turns.length === 0 ? (
              <tr>
                <td colSpan={4}>暂无回合</td>
              </tr>
            ) : (
              turns.slice(0, 40).map((t: any) => (
                <tr key={t.turnId}>
                  <td>{t.turnId?.slice(0, 8)}</td>
                  <td>{t.status}</td>
                  <td>{t.characterId?.slice(0, 8)}</td>
                  <td>{(t.lanes || []).map((l: any) => l.lane).join(", ")}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}
