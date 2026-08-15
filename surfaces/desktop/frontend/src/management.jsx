import { useEffect, useState } from "react";
import {
  ArchiveIcon,
  BarChartIcon,
  ChatBubbleIcon,
  CubeIcon,
  DashboardIcon,
  FileTextIcon,
  IdCardIcon,
  ImageIcon,
  LightningBoltIcon,
  Link2Icon,
  MixerHorizontalIcon,
  PersonIcon,
  ReaderIcon,
  Share2Icon,
} from "@radix-ui/react-icons";
import { Events } from "@wailsio/runtime";
import {
  ActivateManagementCharacter,
  ClearManagementModel,
  ClearManagementProfile,
  ClearManagementSemanticCredential,
  CreateManagementBackup,
  CreateManagementMemory,
  ManagementCharacters,
  ManagementConversation,
  ManagementIntelligence,
  ManagementKnowledge,
  ManagementLogs,
  ManagementMemories,
  ManagementMetrics,
  ManagementModel,
  ManagementOverview,
  ManagementPlugins,
  ManagementProfile,
  ManagementQQ,
  ManagementSemantic,
  ManagementStickers,
  ManagementTrace,
  ManagementTraces,
  ManagementTurnRuntime,
  SaveManagementModel,
  SaveManagementProfile,
  SaveManagementQQ,
  SubscribeManagementLogs,
  TombstoneManagementKnowledge,
  TombstoneManagementMemory,
  UnsubscribeManagementLogs,
} from "../bindings/fairy-desktop/coreservice.js";

const NAV = [
  { id: "overview", label: "概览", icon: DashboardIcon },
  { id: "character", label: "角色", icon: PersonIcon },
  { id: "profile", label: "用户", icon: IdCardIcon },
  { id: "model", label: "模型", icon: MixerHorizontalIcon },
  { id: "stickers", label: "表情包", icon: ImageIcon },
  { id: "integrations", label: "接入", icon: Link2Icon },
  { id: "intelligence", label: "记忆与知识", icon: LightningBoltIcon },
  { id: "plugins", label: "插件", icon: CubeIcon },
  { id: "conversation-debug", label: "对话调试", icon: ChatBubbleIcon },
  { id: "metrics", label: "指标", icon: BarChartIcon },
  { id: "tracing", label: "链路跟踪", icon: Share2Icon },
  { id: "logs", label: "日志", icon: FileTextIcon },
  { id: "backup", label: "备份", icon: ArchiveIcon },
];

function sectionFromHash(hash) {
  const route = hash.replace(/^#\/?/, "").trim().split("?", 1)[0] || "";
  const value = decodeURIComponent(route);
  return NAV.some((item) => item.id === value) ? value : "overview";
}

function sectionHash(section) {
  return `#/${section}`;
}

function hostError(err) {
  if (err && typeof err.message === "string" && err.message !== "") return err.message;
  if (typeof err === "string" && err !== "") return err;
  return "本地宿主请求失败";
}

function Brand() {
  return (
    <div className="brand">
      <span className="brand-mark" aria-hidden="true"><ReaderIcon /></span>
      <div className="brand-copy">
        <strong>FAIRY</strong>
        <small>本地陪伴管理台</small>
      </div>
    </div>
  );
}

function useHost(loader, deps) {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  function reload() {
    setLoading(true);
    return loader().then((value) => {
      setData(value);
      setError("");
    }).catch((err) => {
      setData(null);
      setError(hostError(err));
    }).finally(() => setLoading(false));
  }

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    loader().then((value) => {
      if (cancelled) return;
      setData(value);
      setError("");
    }).catch((err) => {
      if (cancelled) return;
      setData(null);
      setError(hostError(err));
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => { cancelled = true; };
  }, deps);

  return { data, error, loading, reload };
}

function StatusBlock({ error, loading, children }) {
  if (loading) return <p className="management-status">正在读取本地宿主…</p>;
  if (error) return <p className="management-error" role="alert">{error}</p>;
  return children;
}

function Field({ label, children }) {
  return (
    <label className="management-field">
      <span>{label}</span>
      {children}
    </label>
  );
}

function OverviewTask() {
  const { data, error, loading } = useHost(() => ManagementOverview(), []);
  return (
    <StatusBlock error={error} loading={loading}>
      <dl className="management-dl">
        <div><dt>应用</dt><dd>{data?.bootstrap?.appName} {data?.bootstrap?.coreVersion}</dd></div>
        <div><dt>存储</dt><dd>{data?.storage?.storage || data?.storage?.mode} {data?.storage?.ready ? "就绪" : "未就绪"}</dd></div>
        <div><dt>密钥</dt><dd>{data?.secretKey?.ready ? "已配置" : "未就绪"}</dd></div>
        <div><dt>模型</dt><dd>{data?.model?.configured ? `${data.model.model}（已配置）` : "未配置"}</dd></div>
        <div><dt>语义检索</dt><dd>{data?.semanticEmbedding?.configured ? "已配置" : "未配置"}</dd></div>
        {data?.storage?.error ? <div><dt>诊断</dt><dd>{data.storage.error}</dd></div> : null}
      </dl>
    </StatusBlock>
  );
}

function CharacterTask() {
  const { data, error, loading, reload } = useHost(() => ManagementCharacters(), []);
  const [actionError, setActionError] = useState("");
  const characters = data?.characters || [];
  return (
    <StatusBlock error={error} loading={loading}>
      {actionError ? <p className="management-error" role="alert">{actionError}</p> : null}
      <ul className="management-list">
        {characters.map((item) => (
          <li key={item.characterId}>
            <strong>{item.name}</strong>
            <span>{item.characterId} · r{item.revision}{data?.active?.characterId === item.characterId ? " · 当前" : ""}</span>
            {data?.active?.characterId === item.characterId ? null : (
              <button type="button" onClick={() => {
                ActivateManagementCharacter(item.characterId, item.revision).then(() => {
                  setActionError("");
                  reload();
                }).catch((err) => setActionError(hostError(err)));
              }}>激活</button>
            )}
          </li>
        ))}
      </ul>
    </StatusBlock>
  );
}

function ProfileTask() {
  const { data, error, loading, reload } = useHost(() => ManagementProfile(), []);
  const [name, setName] = useState("");
  const [actionError, setActionError] = useState("");
  useEffect(() => {
    if (data && typeof data.preferredName === "string") setName(data.preferredName);
  }, [data]);
  return (
    <StatusBlock error={error} loading={loading}>
      {actionError ? <p className="management-error" role="alert">{actionError}</p> : null}
      <form className="management-form" onSubmit={(event) => {
        event.preventDefault();
        SaveManagementProfile(name).then(() => { setActionError(""); reload(); }).catch((err) => setActionError(hostError(err)));
      }}>
        <Field label="显示名">
          <input value={name} onChange={(event) => setName(event.target.value)} />
        </Field>
        <div className="management-actions">
          <button type="submit">保存</button>
          <button type="button" onClick={() => {
            ClearManagementProfile().then(() => { setName(""); setActionError(""); reload(); }).catch((err) => setActionError(hostError(err)));
          }}>清除</button>
        </div>
      </form>
    </StatusBlock>
  );
}

function ModelTask() {
  const { data, error, loading, reload } = useHost(() => ManagementModel(), []);
  const semantic = useHost(() => ManagementSemantic(), []);
  const [form, setForm] = useState({ protocol: "openai_compatible_api", endpoint: "", model: "", contextWindowTokens: 128000, authMode: "bearer_key", visionInput: false, apiKey: "" });
  const [actionError, setActionError] = useState("");
  useEffect(() => {
    if (!data) return;
    setForm((current) => ({
      ...current,
      protocol: data.protocol || current.protocol,
      endpoint: data.endpoint || "",
      model: data.model || "",
      contextWindowTokens: data.contextWindowTokens || current.contextWindowTokens,
      authMode: data.authMode || current.authMode,
      visionInput: Boolean(data.capabilities?.visionInput),
      apiKey: "",
    }));
  }, [data]);
  return (
    <StatusBlock error={error || semantic.error} loading={loading || semantic.loading}>
      <p className="management-note">凭据只写入宿主，成功后只显示是否已配置。</p>
      {actionError ? <p className="management-error" role="alert">{actionError}</p> : null}
      <dl className="management-dl">
        <div><dt>模型凭据</dt><dd>{data?.configured ? "已配置" : "未配置"}</dd></div>
        <div><dt>语义凭据</dt><dd>{semantic.data?.credentialConfigured ? "已配置" : "未配置"}</dd></div>
      </dl>
      <form className="management-form" onSubmit={(event) => {
        event.preventDefault();
        const apiKey = form.apiKey;
        SaveManagementModel({
          protocol: form.protocol,
          endpoint: form.endpoint,
          model: form.model,
          contextWindowTokens: Number(form.contextWindowTokens),
          authMode: form.authMode,
          visionInput: form.visionInput,
          apiKey,
        }).then(() => {
          setForm((current) => ({ ...current, apiKey: "" }));
          setActionError("");
          reload();
        }).catch((err) => setActionError(hostError(err)));
      }}>
        <Field label="协议"><input value={form.protocol} onChange={(event) => setForm({ ...form, protocol: event.target.value })} /></Field>
        <Field label="端点"><input value={form.endpoint} onChange={(event) => setForm({ ...form, endpoint: event.target.value })} /></Field>
        <Field label="模型"><input value={form.model} onChange={(event) => setForm({ ...form, model: event.target.value })} /></Field>
        <Field label="API Key">
          <input type="password" autoComplete="off" value={form.apiKey} onChange={(event) => setForm({ ...form, apiKey: event.target.value })} />
        </Field>
        <div className="management-actions">
          <button type="submit">保存模型</button>
          <button type="button" onClick={() => {
            ClearManagementModel().then(() => { setForm((current) => ({ ...current, apiKey: "" })); setActionError(""); reload(); }).catch((err) => setActionError(hostError(err)));
          }}>清除模型凭据</button>
          <button type="button" onClick={() => {
            ClearManagementSemanticCredential().then(() => { setActionError(""); semantic.reload(); }).catch((err) => setActionError(hostError(err)));
          }}>清除语义凭据</button>
        </div>
      </form>
    </StatusBlock>
  );
}

function StickersTask() {
  const { data, error, loading } = useHost(() => ManagementStickers(), []);
  const items = data?.items || [];
  return (
    <StatusBlock error={error} loading={loading}>
      <ul className="management-list">
        {items.map((item) => (
          <li key={item.id}><strong>{item.description || item.id}</strong><span>{item.status} · {item.mimeType}</span></li>
        ))}
      </ul>
    </StatusBlock>
  );
}

function IntegrationsTask() {
  const { data, error, loading, reload } = useHost(() => ManagementQQ(), []);
  const [allowlist, setAllowlist] = useState("");
  const [actionError, setActionError] = useState("");
  useEffect(() => {
    if (data?.groupAllowlist) setAllowlist(data.groupAllowlist.join("\n"));
  }, [data]);
  return (
    <StatusBlock error={error} loading={loading}>
      {actionError ? <p className="management-error" role="alert">{actionError}</p> : null}
      <form className="management-form" onSubmit={(event) => {
        event.preventDefault();
        const groupAllowlist = allowlist.split("\n").map((line) => line.trim()).filter(Boolean);
        SaveManagementQQ({ schemaVersion: 1, groupAllowlist }).then(() => { setActionError(""); reload(); }).catch((err) => setActionError(hostError(err)));
      }}>
        <Field label="QQ 群允许名单">
          <textarea rows={6} value={allowlist} onChange={(event) => setAllowlist(event.target.value)} />
        </Field>
        <button type="submit">保存</button>
      </form>
    </StatusBlock>
  );
}

function IntelligenceTask() {
  const snapshot = useHost(() => ManagementIntelligence(), []);
  const memories = useHost(() => ManagementMemories(""), []);
  const knowledge = useHost(() => ManagementKnowledge(), []);
  const [content, setContent] = useState("");
  const [actionError, setActionError] = useState("");
  return (
    <StatusBlock error={snapshot.error || memories.error || knowledge.error} loading={snapshot.loading || memories.loading || knowledge.loading}>
      <dl className="management-dl">
        <div><dt>私人记忆</dt><dd>全局 {snapshot.data?.summary?.activeGlobalMemories || 0} · 角色 {snapshot.data?.summary?.activeCharacterMemories || 0}</dd></div>
        <div><dt>知识</dt><dd>候选 {snapshot.data?.candidateKnowledge || 0} · 已验证 {snapshot.data?.verifiedKnowledge || 0}</dd></div>
        <div><dt>语义</dt><dd>{snapshot.data?.semanticEmbedding?.configured ? "已配置" : "未配置"}</dd></div>
      </dl>
      {actionError ? <p className="management-error" role="alert">{actionError}</p> : null}
      <form className="management-form" onSubmit={(event) => {
        event.preventDefault();
        CreateManagementMemory({ kind: "person_note", scope: { type: "global" }, content, confidenceBasisPoints: 8000 }).then(() => {
          setContent("");
          setActionError("");
          memories.reload();
          snapshot.reload();
        }).catch((err) => setActionError(hostError(err)));
      }}>
        <Field label="新增记忆">
          <textarea rows={3} value={content} onChange={(event) => setContent(event.target.value)} />
        </Field>
        <button type="submit">写入</button>
      </form>
      <ul className="management-list">
        {(memories.data?.global || []).concat(memories.data?.character || []).map((item) => (
          <li key={item.id}>
            <strong>{item.kind}</strong>
            <span>{item.content}</span>
            <button type="button" onClick={() => {
              TombstoneManagementMemory(item.id).then(() => { setActionError(""); memories.reload(); snapshot.reload(); }).catch((err) => setActionError(hostError(err)));
            }}>删除</button>
          </li>
        ))}
        {(knowledge.data?.verified || []).concat(knowledge.data?.candidates || []).map((item) => (
          <li key={item.id}>
            <strong>{item.topic}</strong>
            <span>{item.statement}</span>
            <button type="button" onClick={() => {
              TombstoneManagementKnowledge(item.id).then(() => { setActionError(""); knowledge.reload(); snapshot.reload(); }).catch((err) => setActionError(hostError(err)));
            }}>删除</button>
          </li>
        ))}
      </ul>
    </StatusBlock>
  );
}

function PluginsTask() {
  const { error, loading } = useHost(() => ManagementPlugins(), []);
  return (
    <StatusBlock error={error} loading={loading}>
      <p>插件宿主已就绪。安装与授权将在后续任务提供。</p>
    </StatusBlock>
  );
}

function ConversationTask() {
  const { data, error, loading } = useHost(() => ManagementConversation("", 0, 50), []);
  const [turnID, setTurnID] = useState("");
  const [runtime, setRuntime] = useState(null);
  const [runtimeError, setRuntimeError] = useState("");
  const messages = data?.messages || [];
  return (
    <StatusBlock error={error} loading={loading}>
      <ul className="management-list">
        {messages.map((item) => (
          <li key={item.id}>
            <strong>{item.role}</strong>
            <span>{item.content}</span>
            {item.turnId ? <button type="button" onClick={() => {
              setTurnID(item.turnId);
              ManagementTurnRuntime(item.conversationId || "", item.turnId).then((value) => {
                setRuntime(value);
                setRuntimeError("");
              }).catch((err) => { setRuntime(null); setRuntimeError(hostError(err)); });
            }}>运行时</button> : null}
          </li>
        ))}
      </ul>
      {runtimeError ? <p className="management-error" role="alert">{runtimeError}</p> : null}
      {runtime ? (
        <ol className="management-list">
          {(runtime.events || []).map((item) => (
            <li key={`${item.sequence}-${item.eventType}`}><strong>{item.eventType}</strong><span>#{item.sequence}{item.state ? ` · ${item.state}` : ""}{item.code ? ` · ${item.code}` : ""}</span></li>
          ))}
        </ol>
      ) : turnID ? <p className="management-note">Turn {turnID}</p> : null}
    </StatusBlock>
  );
}

function MetricsTask() {
  const { data, error, loading } = useHost(() => ManagementMetrics(), []);
  return (
    <StatusBlock error={error} loading={loading}>
      <dl className="management-dl">
        <div><dt>Goroutines</dt><dd>{data?.process?.goroutines}</dd></div>
        <div><dt>Heap</dt><dd>{data?.process?.heapAllocBytes}</dd></div>
        <div><dt>消息</dt><dd>收 {data?.messages?.received || 0} · 发 {data?.messages?.sent || 0}</dd></div>
        <div><dt>历史点</dt><dd>{data?.history?.length || 0}</dd></div>
      </dl>
    </StatusBlock>
  );
}

function TracingTask() {
  const [messageID, setMessageID] = useState("");
  const [traceID, setTraceID] = useState("");
  const [search, setSearch] = useState(null);
  const [detail, setDetail] = useState(null);
  const [error, setError] = useState("");
  return (
    <div>
      {error ? <p className="management-error" role="alert">{error}</p> : null}
      <form className="management-form" onSubmit={(event) => {
        event.preventDefault();
        ManagementTraces(messageID).then((value) => { setSearch(value); setError(""); }).catch((err) => { setSearch(null); setError(hostError(err)); });
      }}>
        <Field label="messageId"><input value={messageID} onChange={(event) => setMessageID(event.target.value)} /></Field>
        <button type="submit">搜索 Trace</button>
      </form>
      <ul className="management-list">
        {(search?.traces || []).map((item) => (
          <li key={item.traceId}>
            <strong>{item.traceId}</strong>
            <span>{item.status} · {item.totalDurationMs}ms</span>
            <button type="button" onClick={() => {
              setTraceID(item.traceId);
              ManagementTrace(item.traceId).then((value) => { setDetail(value); setError(""); }).catch((err) => { setDetail(null); setError(hostError(err)); });
            }}>打开</button>
          </li>
        ))}
      </ul>
      {detail ? (
        <ol className="management-list">
          {(detail.spans || []).map((span) => (
            <li key={span.spanId}><strong>{span.operation}</strong><span>{span.status} · {span.durationMs}ms</span></li>
          ))}
        </ol>
      ) : traceID ? <p className="management-note">Trace {traceID}</p> : null}
    </div>
  );
}

function LogsTask() {
  const [entries, setEntries] = useState([]);
  const [error, setError] = useState("");
  useEffect(() => {
    let cancelled = false;
    ManagementLogs().then((snapshot) => {
      if (!cancelled) setEntries(snapshot.entries || []);
    }).catch((err) => { if (!cancelled) setError(hostError(err)); });
    SubscribeManagementLogs().catch((err) => { if (!cancelled) setError(hostError(err)); });
    const off = Events.On("desktop:management-logs", (event) => {
      const entry = event?.data ?? event;
      setEntries((current) => current.concat(entry));
    });
    return () => {
      cancelled = true;
      if (typeof off === "function") off();
      UnsubscribeManagementLogs();
    };
  }, []);
  return (
    <div>
      {error ? <p className="management-error" role="alert">{error}</p> : null}
      <ol className="management-log">
        {entries.map((entry) => (
          <li key={entry.sequence}>
            <time>{entry.timestampUnixMs}</time>
            <strong>{entry.level}</strong>
            <span>{entry.message}</span>
          </li>
        ))}
      </ol>
    </div>
  );
}

function BackupTask() {
  const [result, setResult] = useState(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  return (
    <div>
      <p className="management-note">备份只复制本地 SeekDB 数据目录，不把文件内容回传到前端。</p>
      {error ? <p className="management-error" role="alert">{error}</p> : null}
      <button type="button" disabled={loading} onClick={() => {
        setLoading(true);
        CreateManagementBackup().then((value) => { setResult(value); setError(""); }).catch((err) => { setResult(null); setError(hostError(err)); }).finally(() => setLoading(false));
      }}>{loading ? "正在备份…" : "创建备份"}</button>
      {result ? (
        <dl className="management-dl">
          <div><dt>路径</dt><dd>{result.path}</dd></div>
          <div><dt>文件数</dt><dd>{result.fileCount}</dd></div>
        </dl>
      ) : null}
    </div>
  );
}

function ManagementTask({ section }) {
  const item = NAV.find((entry) => entry.id === section);
  const observability = section === "metrics" || section === "tracing" || section === "logs";
  let body = null;
  switch (section) {
    case "overview": body = <OverviewTask />; break;
    case "character": body = <CharacterTask />; break;
    case "profile": body = <ProfileTask />; break;
    case "model": body = <ModelTask />; break;
    case "stickers": body = <StickersTask />; break;
    case "integrations": body = <IntegrationsTask />; break;
    case "intelligence": body = <IntelligenceTask />; break;
    case "plugins": body = <PluginsTask />; break;
    case "conversation-debug": body = <ConversationTask />; break;
    case "metrics": body = <MetricsTask />; break;
    case "tracing": body = <TracingTask />; break;
    case "logs": body = <LogsTask />; break;
    case "backup": body = <BackupTask />; break;
    default: break;
  }
  return (
    <section
      className={observability ? "observability-page" : "management-task"}
      id={observability ? `observability-panel-${section}` : undefined}
      aria-labelledby="management-task-title"
    >
      <header className="management-task__header">
        <h1 id="management-task-title">{item?.label}</h1>
        <p>此任务由本地宿主提供数据，不要求 Core endpoint 或 bearer。</p>
      </header>
      {body}
    </section>
  );
}

export function ManagementSurface() {
  const [section, setSection] = useState(() => sectionFromHash(window.location.hash));

  useEffect(() => {
    const syncSection = () => setSection(sectionFromHash(window.location.hash));
    window.addEventListener("hashchange", syncSection);
    const parsedSection = sectionFromHash(window.location.hash);
    const hasKnownRoute = window.location.hash.replace(/^#\/?/, "").split("?", 1)[0] === parsedSection;
    if (!hasKnownRoute) window.history.replaceState(null, "", sectionHash(parsedSection));
    return () => window.removeEventListener("hashchange", syncSection);
  }, []);

  function selectSection(next) {
    setSection(next);
    if (window.location.hash !== sectionHash(next)) window.location.hash = sectionHash(next);
  }

  return (
    <div className="shell">
      <aside className="tool-rail">
        <Brand />
        <nav className="nav" aria-label="控制台导航">
          <div className="nav-primary">
            <span className="nav-section-label">工作台</span>
            {NAV.map((item) => {
              const Icon = item.icon;
              const active = section === item.id;
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`nav-item${active ? " active" : ""}`}
                  aria-current={active ? "page" : undefined}
                  onClick={() => selectSection(item.id)}
                >
                  <span className="nav-icon" aria-hidden="true"><Icon /></span>
                  <span className="nav-label">{item.label}</span>
                </button>
              );
            })}
          </div>
        </nav>
      </aside>
      <main className="main">
        <header className="shell-topline" aria-label="控制台状态">
          <div className="topline-path">
            <span>管理工作区</span>
            <i>/</i>
            <strong>{NAV.find((item) => item.id === section)?.label}</strong>
          </div>
          <div className="topline-health">
            <span className="core-status-dot" aria-hidden="true" />
            <strong>本地运行时</strong>
          </div>
        </header>
        <div className="main-canvas">
          <ManagementTask section={section} />
        </div>
      </main>
    </div>
  );
}
