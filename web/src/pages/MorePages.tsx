import { Button, Select, Switch, Text, TextArea, TextField } from "@radix-ui/themes";
import { ReloadIcon } from "@radix-ui/react-icons";
import { useEffect, useState } from "react";
import { api } from "../api";
import { ConfigSection, EmptyState, Field, PageHeader } from "../components/ui";

const roleSummaryLimit = 160;

export function summarizeRoleDescription(value: unknown): string {
  if (typeof value !== "string") return "在角色库创建并激活角色后，她会成为 Fairy 的对话身份。";

  const normalized = value.replace(/\s+/gu, " ").trim();
  if (!normalized) return "在角色库补充角色描述后，这里会显示简短的身份摘要。";

  const sentences = normalized.match(/[^。！？.!?]+[。！？.!?]+|[^。！？.!?]+$/gu) || [normalized];
  const excerpt = sentences.slice(0, 2).map((sentence) => sentence.trim()).join(" ");
  const characters = Array.from(excerpt);
  const sourceWasShortened = excerpt.length < normalized.length || characters.length > roleSummaryLimit;
  const bounded = characters.slice(0, roleSummaryLimit).join("").trim().replace(/[，、；,:：;。！？.!?]+$/u, "");

  return sourceWasShortened ? `${bounded}…` : bounded;
}

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
  const web = status?.webSearch || {};
  const semantic = status?.semanticEmbedding || {};
  const active = characters?.active;
  const roleSummary = active
    ? summarizeRoleDescription(active.description)
    : "在角色库创建并激活角色后，她会成为 Fairy 的对话身份。";
  const coreReady = Boolean(status);

  const systems = [
    {
      title: "模型",
      ready: Boolean(model.configured),
      value: model.configured ? model.model || "已连接" : "尚未配置",
      detail: model.endpoint || "保存服务地址与模型后即可对话",
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
      value: semantic.provider && semantic.provider !== "none" ? semantic.provider : "仅全文检索",
      detail: semantic.model || "默认关键词检索",
    },
  ];

  return (
    <section className="overview-page">
      <PageHeader
        title="概览"
        description="查看当前角色、Core 状态和最近能力配置。"
        action={
          <Button variant="soft" onClick={() => void reload().then(() => onToast("已刷新")).catch((e) => onToast(e.message, true))}>
            <ReloadIcon />刷新
          </Button>
        }
      />
      <div className="overview-dashboard">
        <article className="companion-summary">
          <div className="identity-glyph" aria-hidden="true">
            {active?.name?.trim()?.slice(0, 1) || "F"}
          </div>
          <div className="identity-copy">
            <span className="identity-label">当前角色</span>
            <h2>{active?.name || "还没有激活角色"}</h2>
            <p>{roleSummary}</p>
            <div className="identity-meta">
              <span className={`identity-state ${active ? "ready" : "pending"}`}>
                {active ? "角色已就绪" : "等待角色"}
              </span>
              <span>完整设定在角色页维护</span>
            </div>
          </div>
        </article>

        <aside className="runtime-summary" aria-label="运行状态">
          <header className="runtime-heading">
            <div>
              <span>运行状态</span>
              <strong>{coreReady ? "Core 运行正常" : "正在读取状态"}</strong>
            </div>
            <span className={`system-state ${coreReady ? "ready" : "pending"}`}>
              {coreReady ? "已连接" : "读取中"}
            </span>
          </header>
          <div className="runtime-list">
            {systems.map((system) => (
              <div key={system.title} className="runtime-row">
                <div>
                  <span>{system.title}</span>
                  <small>{system.detail}</small>
                </div>
                <div className="runtime-value">
                  <strong>{system.value}</strong>
                  <span className={`system-state ${system.ready ? "ready" : "pending"}`}>
                    {system.ready ? "已就绪" : "待配置"}
                  </span>
                </div>
              </div>
            ))}
          </div>
        </aside>
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
    <section className="intelligence-page">
      <PageHeader
        title="记忆与知识"
        description="管理本机记忆、知识召回和公开资料检索。"
        status={intel?.ready ? "本地层已就绪" : "本地层不可用"}
        ready={Boolean(intel?.ready)}
      />
      <div className="ledger-summary" aria-label="记忆与任务摘要">
        {[
          ["全局记忆", summary.activeGlobalMemories],
          ["角色关系", summary.activeCharacterMemories],
          ["待审记忆", summary.needsReviewMemories],
          ["后台任务", intel?.activeBackgroundJobs ?? 0],
        ].map(([label, value]) => (
          <div key={String(label)} className="ledger-summary-item">
            <span>{label}</span>
            <strong>{value ?? "未获取"}</strong>
          </div>
        ))}
      </div>

      <div className="intelligence-workspace">
        <ConfigSection
          title="公开资料检索"
          description="Compose 环境默认通过 OpenSERP 获取公开网页结果。"
          className="web-search-settings"
        >
          <div className="settings-row">
            <div>
              <Text weight="medium">允许检索公开资料</Text>
              <Text as="p" size="1" color="gray">
                关闭后只使用本机记忆和知识。
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
          <Field label="检索服务地址">
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
        </ConfigSection>

        <ConfigSection
          title="个人记忆台账"
          description="手动写入需要已有会话回合；没有对话时仍可浏览现有条目。"
          className="memory-ledger"
        >
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
              <EmptyState title="暂无记忆条目" description="写入后会在这里按范围显示，不预留空白滚动区域。" />
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
        </ConfigSection>
      </div>
    </section>
  );
}
