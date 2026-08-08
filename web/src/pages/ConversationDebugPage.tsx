import { Badge, Button, Dialog, Select, Tabs, TextArea } from "@radix-ui/themes";
import { CopyIcon, Cross2Icon, EnterFullScreenIcon, PaperPlaneIcon, PlusIcon, ReloadIcon, StopIcon } from "@radix-ui/react-icons";
import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { api } from "../api";
import {
  DebugSessionClient,
  type LaneModelUsage,
  type SessionOpened,
  type TurnEvent,
} from "../sessionClient";
import { EmptyState, InlineNotice, PageHeader } from "../components/ui";
import {
  projectRuntimeTimeline,
  runtimeEventSummary,
  runtimeModelUsageTotals,
  runtimeToolDetail,
  type RuntimeEvent,
  type RuntimeToolContext,
  type RuntimeTimelineMode,
} from "../runtimeTimeline";

type Character = {
  characterId: string;
  name: string;
};

type Catalog = {
  characters: Character[];
  active: Character | null;
};

type ExpressionPart = {
  kind: "utterance" | "sticker";
  text?: string;
  visualState: string;
  sticker?: {
    id: string;
    description: string;
    mimeType: string;
  };
};

type MessageRecord = {
  id: string;
  messageId?: string;
  turnId: string;
  sequence: number;
  role: "user" | "assistant";
  content: string;
  parts?: ExpressionPart[];
  createdAtUnixMs: number;
  optimistic?: boolean;
};

type MessageBubble =
  | { kind: "utterance"; text: string }
  | { kind: "sticker"; description: string };

type MessagePage = { messages: MessageRecord[] };

type RuntimeResponse = {
  conversationId: string;
  turnId: string;
  events: RuntimeEvent[];
};

type DebugTurn = {
  localId: string;
  turnId: string;
  input: string;
  preview: string;
  state: string;
  startedAt: number;
  completedAt?: number;
  usage?: LaneModelUsage[];
  error?: string;
  runtime?: RuntimeEvent[];
  runtimeState?: "loading" | "ready" | "error";
};

type ConnectionState = "loading" | "connecting" | "ready" | "disconnected" | "error";

const DEBUG_ENDPOINT_STORAGE_PREFIX = "fairy.console.debug.endpoint.v1.";

const STATE_LABELS: Record<string, string> = {
  submitted: "已提交",
  interpreting: "理解输入",
  gathering: "召回上下文",
  planning: "规划回复",
  responding: "生成回复",
  completed: "已完成",
  interrupted: "已中断",
  failed: "失败",
  unknown: "状态未知",
};

const EVENT_LABELS: Record<string, string> = {
  transition: "阶段转换",
  gather: "上下文收集",
  prompt: "上下文组装",
  continuation: "上下文续接",
  model: "模型调用",
  tool: "工具执行",
  context_window: "上下文窗口",
  context_compaction: "上下文压缩",
  compile: "回复编译",
  beat_delivery: "回复交付",
  terminal: "Turn 终态",
};

function createID(prefix: string) {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `${prefix}-${crypto.randomUUID()}`;
  }
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function validDebugEndpointKey(value: string) {
  return value.length >= 16 && value.length <= 200 && /^[A-Za-z0-9._-]+$/.test(value);
}

function syncLegacyDebugEndpointKey(storageKey: string, endpointKey: string) {
  try {
    window.sessionStorage.setItem(storageKey, endpointKey);
  } catch {
    // The durable copy is authoritative; this write only keeps rollback compatibility.
  }
}

function persistDebugEndpointKey(storageKey: string, endpointKey: string) {
  try {
    window.localStorage.setItem(storageKey, endpointKey);
    if (window.localStorage.getItem(storageKey) !== endpointKey) {
      throw new Error("debug endpoint key was not retained");
    }
  } catch {
    throw new Error("浏览器持久存储不可用，无法打开可恢复的调试会话");
  }
  syncLegacyDebugEndpointKey(storageKey, endpointKey);
}

function resolveDebugEndpointKey(characterId: string, fresh: boolean) {
  const storageKey = `${DEBUG_ENDPOINT_STORAGE_PREFIX}${encodeURIComponent(characterId)}`;
  try {
    if (!fresh) {
      const stored = window.localStorage.getItem(storageKey)?.trim() || "";
      if (validDebugEndpointKey(stored)) {
        syncLegacyDebugEndpointKey(storageKey, stored);
        return stored;
      }
    }
  } catch {
    if (!fresh) {
      throw new Error("浏览器持久存储不可用，无法恢复调试会话");
    }
  }

  if (!fresh) {
    try {
      const legacy = window.sessionStorage.getItem(storageKey)?.trim() || "";
      if (validDebugEndpointKey(legacy)) {
        persistDebugEndpointKey(storageKey, legacy);
        return legacy;
      }
    } catch {
      // Missing legacy state does not prevent creating a new durable identity.
    }
  }

  const next = createID("web-evaluation");
  persistDebugEndpointKey(storageKey, next);
  return next;
}

function mergeMessageRecords(current: MessageRecord[], incoming: MessageRecord[]) {
  const persistedTurnRoles = new Set(incoming.map((message) => `${message.turnId}\u0000${message.role}`));
  const merged = new Map<string, MessageRecord>();
  for (const message of current) {
    if (message.optimistic && persistedTurnRoles.has(`${message.turnId}\u0000${message.role}`)) continue;
    merged.set(message.id, message);
  }
  for (const message of incoming) merged.set(message.id, { ...message, optimistic: false });
  return [...merged.values()].sort((left, right) =>
    left.sequence - right.sequence || left.createdAtUnixMs - right.createdAtUnixMs || left.id.localeCompare(right.id));
}

function isTerminal(state: string) {
  return state === "completed" || state === "interrupted" || state === "failed";
}

function projectMessageBubbles(message: MessageRecord): MessageBubble[] {
  if (message.role === "user") {
    return [{ kind: "utterance", text: message.content }];
  }

  const bubbles = (message.parts || []).flatMap<MessageBubble>((part) => {
    if (part.kind === "utterance" && part.text?.trim()) {
      return [{ kind: "utterance", text: part.text }];
    }
    if (part.kind === "sticker" && part.sticker?.description.trim()) {
      return [{ kind: "sticker", description: part.sticker.description }];
    }
    return [];
  });
  if (bubbles.length > 0) return bubbles;
  return message.content.trim() ? [{ kind: "utterance", text: message.content }] : [];
}

function previewText(payload: Record<string, unknown>) {
  if (payload.type === "reply.preview" && Array.isArray(payload.chains)) {
    return payload.chains
      .map((chain) => (chain && typeof chain === "object" && "text" in chain ? String(chain.text || "") : ""))
      .filter(Boolean)
      .join("\n");
  }
  if (payload.type === "beat.ready" && typeof payload.displayText === "string") {
    return payload.displayText;
  }
  if (payload.type === "completed" && typeof payload.text === "string") {
    return payload.text;
  }
  return "";
}

function formatDuration(turn: DebugTurn | undefined, now: number) {
  if (!turn) return "不可用";
  const endedAt = turn.completedAt ?? (isTerminal(turn.state) ? null : now);
  if (endedAt === null) return "不可用";
  const elapsed = Math.max(0, endedAt - turn.startedAt);
  return elapsed < 1000 ? `${elapsed} ms` : `${(elapsed / 1000).toFixed(2)} s`;
}

function usageSummary(usage?: LaneModelUsage[]) {
  if (!usage?.length) return { input: null, output: null, cached: null };
  let input = 0;
  let output = 0;
  let cached = 0;
  let hasInput = false;
  let hasOutput = false;
  let hasCached = false;
  for (const lane of usage) {
    if (typeof lane.usage.inputTokens === "number") {
      input += lane.usage.inputTokens;
      hasInput = true;
    }
    if (typeof lane.usage.outputTokens === "number") {
      output += lane.usage.outputTokens;
      hasOutput = true;
    }
    if (lane.usage.cachedInputTokens?.status === "observed" && typeof lane.usage.cachedInputTokens.tokens === "number") {
      cached += lane.usage.cachedInputTokens.tokens;
      hasCached = true;
    }
  }
  return {
    input: hasInput ? input : null,
    output: hasOutput ? output : null,
    cached: hasCached ? cached : null,
  };
}

function hydrateTurns(messages: MessageRecord[]): DebugTurn[] {
  const byTurn = new Map<string, DebugTurn>();
  for (const message of messages) {
    const existing = byTurn.get(message.turnId);
    if (message.role === "user") {
      byTurn.set(message.turnId, {
        localId: message.turnId,
        turnId: message.turnId,
        input: message.content,
        preview: existing?.preview || "",
        state: existing?.state || "unknown",
        startedAt: message.createdAtUnixMs,
        completedAt: existing?.completedAt,
      });
    } else if (existing) {
      existing.preview = message.content;
      existing.state = "completed";
      existing.completedAt = message.createdAtUnixMs;
    }
  }
  return [...byTurn.values()];
}

function safeSourceURL(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:" ? url.toString() : "";
  } catch {
    return "";
  }
}

function toolContextCount(context: RuntimeToolContext | null) {
  if (!context) return 0;
  return context.personalMemories.length + context.knowledge.length + context.socialMemories.length;
}

function toolEventScopeSummary(event: RuntimeEvent) {
  const detail = runtimeToolDetail(event);
  if (!detail) return "历史事件，仅保留状态摘要";
  return `返回 ${toolContextCount(detail.result)} 条，注入 ${toolContextCount(detail.mergedContext)} 条`;
}

function ToolContextDetails({ title, context }: { title: string; context: RuntimeToolContext | null }) {
  if (!context) {
    return (
      <section className="debug-tool-context empty">
        <header className="debug-tool-context-summary"><strong>{title}</strong></header>
        <p>该状态未留存上下文正文。</p>
      </section>
    );
  }
  const total = toolContextCount(context);
  return (
    <section className="debug-tool-context">
      <header className="debug-tool-context-summary">
        <div>
          <strong>{title}</strong>
          <span>共 {total} 条，个人 {context.personalMemories.length}，知识 {context.knowledge.length}，社交 {context.socialMemories.length}</span>
        </div>
        {context.semanticStatus ? <span>语义检索：{context.semanticStatus}</span> : null}
      </header>
      {total === 0 ? <div className="debug-tool-empty">没有可展示的上下文条目。</div> : null}

      {context.personalMemories.length > 0 ? (
        <section className="debug-tool-group">
          <header><h3>个人记忆</h3><span>{context.personalMemories.length}</span></header>
          <div className="debug-tool-items">
            {context.personalMemories.map((item, index) => (
              <article key={`personal-${item.id || index}`} className="debug-tool-item">
                <span>{item.kind || "未分类"}</span>
                <p>{item.content || "空内容"}</p>
              </article>
            ))}
          </div>
        </section>
      ) : null}

      {context.knowledge.length > 0 ? (
        <section className="debug-tool-group">
          <header><h3>知识</h3><span>{context.knowledge.length}</span></header>
          <div className="debug-tool-items">
            {context.knowledge.map((item, index) => (
              <article key={`knowledge-${item.id || index}`} className="debug-tool-item knowledge">
                <span>{item.topic || "未分类"}</span>
                <p>{item.statement || "空内容"}</p>
                {item.sources.length > 0 ? (
                  <ul className="debug-tool-sources">
                    {item.sources.map((source, sourceIndex) => {
                      const href = safeSourceURL(source.url);
                      return (
                        <li key={`${source.url}-${sourceIndex}`}>
                          <div>
                            <span>来源 {source.rank ?? sourceIndex + 1}</span>
                            {href ? <a href={href} target="_blank" rel="noreferrer">{source.title || source.url}</a> : <strong>{source.title || "无有效链接"}</strong>}
                          </div>
                          {source.snippet && source.snippet !== item.statement ? <p>{source.snippet}</p> : null}
                        </li>
                      );
                    })}
                  </ul>
                ) : null}
              </article>
            ))}
          </div>
        </section>
      ) : null}

      {context.socialMemories.length > 0 ? (
        <section className="debug-tool-group">
          <header><h3>社交记忆</h3><span>{context.socialMemories.length}</span></header>
          <div className="debug-tool-items">
            {context.socialMemories.map((item, index) => (
              <article key={`social-${item.id || index}`} className="debug-tool-item">
                <span>{item.kind || "未分类"}{item.situation ? ` / ${item.situation}` : ""}</span>
                <p>{item.content || "空内容"}</p>
              </article>
            ))}
          </div>
        </section>
      ) : null}
    </section>
  );
}

function ToolRuntimeDialog({ event, onClose }: { event: RuntimeEvent | null; onClose: () => void }) {
  if (!event) return null;
  const detail = runtimeToolDetail(event);
  const toolName = typeof event.metadata?.tool === "string" ? event.metadata.tool : "未知工具";
  const phase = typeof event.metadata?.phase === "string" ? event.metadata.phase : "未记录";
  const status = detail?.status || (typeof event.metadata?.status === "string" ? event.metadata.status : "状态未知");
  const resultCount = toolContextCount(detail?.result || null);
  const mergedCount = toolContextCount(detail?.mergedContext || null);

  return (
    <Dialog.Root open onOpenChange={(open) => { if (!open) onClose(); }}>
      <Dialog.Content className="debug-tool-dialog" aria-describedby="debug-tool-dialog-description">
        <header className="debug-tool-dialog-heading">
          <div>
            <Dialog.Title>工具执行详情</Dialog.Title>
            <Dialog.Description id="debug-tool-dialog-description">查看工具返回以及下一次模型调用实际使用的上下文。</Dialog.Description>
          </div>
          <Dialog.Close>
            <Button className="debug-tool-dialog-close" size="2" variant="ghost" aria-label="关闭工具详情">
              <Cross2Icon />
            </Button>
          </Dialog.Close>
        </header>

        <dl className="debug-tool-dialog-meta">
          <div><dt>工具</dt><dd>{toolName}</dd></div>
          <div><dt>阶段</dt><dd>{phase}</dd></div>
          <div><dt>状态</dt><dd>{status}</dd></div>
          <div><dt>数据</dt><dd>返回 {resultCount} 条，注入 {mergedCount} 条</dd></div>
        </dl>

        {!detail ? (
          <div className="debug-tool-legacy">
            <strong>仅有状态摘要</strong>
            <p>该 Turn 创建时未记录工具明细，无法还原调用参数和注入上下文。</p>
          </div>
        ) : (
          <>
            <section className="debug-tool-query">
              <span>查询参数</span>
              <p>{detail.query || "未记录有效查询参数"}</p>
            </section>
            <Tabs.Root key={event.sequence} className="debug-tool-tabs" defaultValue="result">
              <Tabs.List aria-label="工具上下文视图">
                <Tabs.Trigger value="result">本次工具返回 <span>{resultCount}</span></Tabs.Trigger>
                <Tabs.Trigger value="merged">最终注入上下文 <span>{mergedCount}</span></Tabs.Trigger>
              </Tabs.List>
              <Tabs.Content value="result">
                <ToolContextDetails title="本次工具返回" context={detail.result} />
              </Tabs.Content>
              <Tabs.Content value="merged">
                <ToolContextDetails title="最终注入上下文" context={detail.mergedContext} />
              </Tabs.Content>
            </Tabs.Root>
          </>
        )}
      </Dialog.Content>
    </Dialog.Root>
  );
}

export function TurnRuntimeTimeline({
  events,
  state,
  terminal,
  onRetry,
}: {
  events: RuntimeEvent[];
  state: "loading" | "ready" | "error" | undefined;
  terminal: boolean;
  onRetry?: () => void;
}) {
  const [mode, setMode] = useState<RuntimeTimelineMode>("summary");
  const [selectedToolSequence, setSelectedToolSequence] = useState<number | null>(null);
  const selectedToolTriggerRef = useRef<HTMLButtonElement | null>(null);
  const projected = useMemo(() => projectRuntimeTimeline(events, mode), [events, mode]);
  const selectedToolEvent = selectedToolSequence === null
    ? null
    : events.find((event) => event.eventType === "tool" && event.sequence === selectedToolSequence) || null;

  useEffect(() => {
    setSelectedToolSequence(null);
    selectedToolTriggerRef.current = null;
  }, [events]);

  function closeToolRuntimeDialog() {
    const trigger = selectedToolTriggerRef.current;
    setSelectedToolSequence(null);
    window.setTimeout(() => trigger?.focus(), 0);
  }

  return (
    <>
      <section className="debug-runtime-section">
        <div className="debug-runtime-heading">
          <div>
            <strong>{mode === "raw" ? "原始事件" : "关键时间线"}</strong>
            <span>{events.length ? `关键 ${projectRuntimeTimeline(events, "summary").length} / 原始 ${events.length}` : "隐私安全投影"}</span>
          </div>
          {events.length ? (
            <Button className="debug-runtime-toggle" size="1" variant="ghost" aria-pressed={mode === "raw"} onClick={() => setMode((current) => current === "summary" ? "raw" : "summary")}>
              {mode === "raw" ? "返回关键时间线" : `查看原始事件 (${events.length})`}
            </Button>
          ) : null}
          {state === "error" && onRetry ? <Button size="1" variant="ghost" onClick={onRetry}><ReloadIcon /> 重试</Button> : null}
        </div>
        {state === "loading" ? (
          <p className="debug-runtime-state">正在读取运行记录…</p>
        ) : state === "error" ? (
          <p className="debug-runtime-state error">运行记录读取失败</p>
        ) : !events.length ? (
          <p className="debug-runtime-state">{terminal ? "暂无运行记录" : "正在收集运行记录…"}</p>
        ) : (
          <ol className="debug-runtime-list" data-runtime-mode={mode}>
            {projected.map((event, index) => {
              const isTool = event.eventType === "tool";
              return (
                <li key={`${event.sequence}-${event.eventType}`}>
                  <span>{mode === "raw" ? event.sequence : index + 1}</span>
                  <div className="debug-runtime-entry">
                    {isTool ? (
                      <button
                        type="button"
                        className="debug-tool-toggle"
                        aria-haspopup="dialog"
                        onClick={(clickEvent) => {
                          selectedToolTriggerRef.current = clickEvent.currentTarget;
                          setSelectedToolSequence(event.sequence);
                        }}
                      >
                        <span>
                          <strong>{EVENT_LABELS[event.eventType] || event.eventType}</strong>
                          <p>{runtimeEventSummary(event)}</p>
                          <small>{toolEventScopeSummary(event)}</small>
                        </span>
                        <EnterFullScreenIcon aria-hidden="true" />
                      </button>
                    ) : (
                      <>
                        <strong>{EVENT_LABELS[event.eventType] || event.eventType}</strong>
                        <p>{runtimeEventSummary(event)}</p>
                      </>
                    )}
                  </div>
                  <time>{new Date(event.createdAtUnixMs).toLocaleTimeString("zh-CN", { hour12: false })}</time>
                </li>
              );
            })}
          </ol>
        )}
      </section>
      <ToolRuntimeDialog event={selectedToolEvent} onClose={closeToolRuntimeDialog} />
    </>
  );
}

export function ConversationDebugPage({ onOpenCharacters }: { onOpenCharacters: () => void }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [catalogError, setCatalogError] = useState("");
  const [selectedCharacterId, setSelectedCharacterId] = useState("");
  const [connection, setConnection] = useState<ConnectionState>("loading");
  const [connectionError, setConnectionError] = useState("");
  const [opened, setOpened] = useState<SessionOpened | null>(null);
  const [messages, setMessages] = useState<MessageRecord[]>([]);
  const [turns, setTurns] = useState<DebugTurn[]>([]);
  const [selectedTurnId, setSelectedTurnId] = useState("");
  const [activeTurnId, setActiveTurnId] = useState("");
  const [draft, setDraft] = useState("");
  const [copiedMessageId, setCopiedMessageId] = useState("");
  const [clockNow, setClockNow] = useState(() => Date.now());
  const clientRef = useRef<DebugSessionClient | null>(null);
  const endpointIdentityRef = useRef({ characterId: "", endpointKey: "" });
  const generationRef = useRef(0);
  const messageRequestRef = useRef(0);
  const pendingLocalMessageRef = useRef("");
  const runtimeRequestRef = useRef<Map<string, number>>(new Map());
  const transcriptRef = useRef<HTMLDivElement | null>(null);

  const selectedCharacter = catalog?.characters.find((character) => character.characterId === selectedCharacterId) || null;
  const selectedTurn = turns.find((turn) => (turn.turnId || turn.localId) === selectedTurnId) || turns.at(-1);
  const selectedTurnTerminal = selectedTurn ? isTerminal(selectedTurn.state) : false;
  const finalUsage = usageSummary(selectedTurn?.usage);
  const runtimeUsage = useMemo(
    () => runtimeModelUsageTotals(selectedTurn?.runtime || []),
    [selectedTurn?.runtime],
  );
  const selectedUsage = selectedTurnTerminal ? finalUsage : {
    input: finalUsage.input ?? runtimeUsage.input,
    output: finalUsage.output ?? runtimeUsage.output,
    cached: finalUsage.cached ?? runtimeUsage.cached,
  };
  const metricPlaceholder = selectedTurnTerminal ? "不可用" : "统计中";
  const activeRuntimeTurnId = useMemo(() => {
    if (!activeTurnId) return "";
    return turns.find((turn) => turn.turnId === activeTurnId || turn.localId === activeTurnId)?.turnId || "";
  }, [activeTurnId, turns]);

  useEffect(() => {
    if (!selectedTurn || isTerminal(selectedTurn.state)) return;
    setClockNow(Date.now());
    const timer = window.setInterval(() => setClockNow(Date.now()), 250);
    return () => window.clearInterval(timer);
  }, [selectedTurn?.localId, selectedTurn?.turnId, selectedTurn?.state]);

  async function loadMessages(conversationId: string, generation: number) {
    const requestSequence = messageRequestRef.current + 1;
    messageRequestRef.current = requestSequence;
    const page = await api<MessagePage>(`/sessions/${conversationId}/messages?limit=200`);
    if (generation !== generationRef.current || requestSequence !== messageRequestRef.current) return;
    setMessages((current) => mergeMessageRecords(current, page.messages || []));
  }

  async function loadRuntime(conversationId: string, turnId: string, generation: number, silent = false) {
    const requestKey = `${generation}:${turnId}`;
    const requestSequence = (runtimeRequestRef.current.get(requestKey) || 0) + 1;
    runtimeRequestRef.current.set(requestKey, requestSequence);
    if (!silent) {
      setTurns((current) => current.map((turn) => turn.turnId === turnId ? { ...turn, runtimeState: "loading" } : turn));
    }
    try {
      const response = await api<RuntimeResponse>(`/sessions/${conversationId}/turns/${turnId}/runtime`);
      if (generation !== generationRef.current || runtimeRequestRef.current.get(requestKey) !== requestSequence) return;
      setTurns((current) => current.map((turn) => turn.turnId === turnId
        ? { ...turn, runtime: response.events || [], runtimeState: "ready" }
        : turn));
    } catch {
      if (generation !== generationRef.current || runtimeRequestRef.current.get(requestKey) !== requestSequence) return;
      if (!silent) {
        setTurns((current) => current.map((turn) => turn.turnId === turnId ? { ...turn, runtimeState: "error" } : turn));
      }
    }
  }

  function handleTurnEvent(event: TurnEvent, generation: number) {
    if (generation !== generationRef.current) return;
    const payload = event.payload || {};
    const preview = previewText(payload);
    const terminal = isTerminal(event.state);
    const error = payload.type === "failed" && payload.error && typeof payload.error === "object" && "message" in payload.error
      ? String(payload.error.message || "Turn 执行失败")
      : undefined;
    const usage = payload.type === "completed" && Array.isArray(payload.usage)
      ? payload.usage as LaneModelUsage[]
      : undefined;

    const pendingLocalMessageId = pendingLocalMessageRef.current;
    if (pendingLocalMessageId) {
      setMessages((current) => current.map((message) => message.id === pendingLocalMessageId
        ? { ...message, turnId: event.turnId }
        : message));
    }

    setTurns((current) => {
      let index = current.findIndex((turn) => turn.turnId === event.turnId);
      if (index < 0) {
        for (let candidate = current.length - 1; candidate >= 0; candidate -= 1) {
          if (!current[candidate].turnId && !isTerminal(current[candidate].state)) {
            index = candidate;
            break;
          }
        }
      }
      if (index < 0) return current;
      const next = [...current];
      const previous = next[index];
      next[index] = {
        ...previous,
        turnId: event.turnId,
        state: event.state,
        preview: preview || previous.preview,
        completedAt: terminal ? Date.now() : previous.completedAt,
        usage: usage || previous.usage,
        error: error || previous.error,
      };
      return next;
    });
    setActiveTurnId(terminal ? "" : event.turnId);
    setSelectedTurnId(event.turnId);
    if (terminal) {
      pendingLocalMessageRef.current = "";
      void loadMessages(event.conversationId, generation).catch(() => undefined);
    }
  }

  async function connect(characterId: string, fresh: boolean) {
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    messageRequestRef.current += 1;
    clientRef.current?.close();
    clientRef.current = null;
    setConnection("connecting");
    setConnectionError("");
    setOpened(null);
    setActiveTurnId("");
    pendingLocalMessageRef.current = "";
    try {
      const endpointKey = resolveDebugEndpointKey(characterId, fresh);
      const identityChanged = endpointIdentityRef.current.characterId !== characterId
        || endpointIdentityRef.current.endpointKey !== endpointKey;
      endpointIdentityRef.current = { characterId, endpointKey };
      if (fresh || identityChanged) {
        setMessages([]);
        setTurns([]);
        setSelectedTurnId("");
      }
      const result = await DebugSessionClient.connect(characterId, endpointKey, {
        onTurnEvent: (event) => handleTurnEvent(event, generation),
        onDisconnect: (reason) => {
          if (generation !== generationRef.current) return;
          setConnection("disconnected");
          setConnectionError(reason);
          setActiveTurnId("");
        },
      });
      if (generation !== generationRef.current) {
        result.client.close();
        return;
      }
      clientRef.current = result.client;
      setOpened(result.opened);
      setConnection("ready");
      const requestSequence = messageRequestRef.current + 1;
      messageRequestRef.current = requestSequence;
      const page = await api<MessagePage>(`/sessions/${result.opened.conversationId}/messages?limit=200`);
      if (generation !== generationRef.current || requestSequence !== messageRequestRef.current) return;
      setMessages((current) => mergeMessageRecords(current, page.messages || []));
      const hydrated = hydrateTurns(page.messages || []);
      setTurns(hydrated);
      setSelectedTurnId(hydrated.at(-1)?.turnId || "");
    } catch (error) {
      if (generation !== generationRef.current) return;
      setConnection("error");
      setConnectionError(error instanceof Error ? error.message : "无法建立调试会话");
    }
  }

  useEffect(() => {
    let disposed = false;
    api<Catalog>("/characters")
      .then((next) => {
        if (disposed) return;
        setCatalog(next);
        const pick = next.active?.characterId || next.characters[0]?.characterId || "";
        setSelectedCharacterId(pick);
        setConnection(pick ? "connecting" : "loading");
        if (pick) void connect(pick, false);
      })
      .catch((error) => {
        if (disposed) return;
        setCatalogError(error instanceof Error ? error.message : "角色读取失败");
        setConnection("error");
      });
    return () => {
      disposed = true;
      generationRef.current += 1;
      clientRef.current?.close();
      clientRef.current = null;
    };
  }, []);

  useEffect(() => {
    const conversationId = opened?.conversationId;
    if (!conversationId || !activeRuntimeTurnId) return;
    const generation = generationRef.current;
    let disposed = false;
    const refresh = () => {
      if (!disposed) void loadRuntime(conversationId, activeRuntimeTurnId, generation, true);
    };
    refresh();
    const timer = window.setInterval(refresh, 1000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, [opened?.conversationId, activeRuntimeTurnId]);

  useEffect(() => {
    const conversationId = opened?.conversationId;
    if (!conversationId || !selectedTurn?.turnId || selectedTurn.runtimeState) return;
    void loadRuntime(conversationId, selectedTurn.turnId, generationRef.current);
  }, [opened?.conversationId, selectedTurn?.turnId, selectedTurn?.runtimeState]);

  useEffect(() => {
    const element = transcriptRef.current;
    if (element) element.scrollTop = element.scrollHeight;
  }, [messages, turns]);

  const pendingPreview = useMemo(() => {
    if (!activeTurnId) return "";
    return turns.find((turn) => turn.turnId === activeTurnId)?.preview || "";
  }, [activeTurnId, turns]);

  async function submit() {
    const input = draft.trim();
    if (!input || !opened || !clientRef.current || connection !== "ready" || activeTurnId) return;
    const localId = createID("turn");
    const messageId = createID("debug-msg");
    pendingLocalMessageRef.current = localId;
    setDraft("");
    setTurns((current) => [...current, {
      localId,
      turnId: "",
      input,
      preview: "",
      state: "submitted",
      startedAt: Date.now(),
    }]);
    setSelectedTurnId(localId);
    setActiveTurnId(localId);
    setMessages((current) => [...current, {
      id: localId,
      messageId,
      turnId: localId,
      sequence: current.length + 1,
      role: "user",
      content: input,
      createdAtUnixMs: Date.now(),
      optimistic: true,
    }]);
    try {
      await clientRef.current.submitTurn(opened.conversationId, input, messageId);
    } catch (error) {
      setTurns((current) => current.map((turn) => turn.localId === localId && !turn.turnId
        ? { ...turn, state: "failed", completedAt: Date.now(), error: error instanceof Error ? error.message : "提交失败" }
        : turn));
      if (pendingLocalMessageRef.current === localId) pendingLocalMessageRef.current = "";
      setActiveTurnId("");
    }
  }

  async function copyMessageId(messageId: string) {
    try {
      await navigator.clipboard.writeText(messageId);
      setCopiedMessageId(messageId);
      window.setTimeout(() => setCopiedMessageId((current) => current === messageId ? "" : current), 1600);
    } catch {
      setConnectionError("无法复制 messageId，请检查浏览器剪贴板权限");
    }
  }

  async function cancel() {
    if (!opened || !clientRef.current || !activeTurnId) return;
    const turn = turns.find((item) => item.turnId === activeTurnId);
    if (!turn?.turnId) return;
    try {
      await clientRef.current.cancelTurn(opened.conversationId, turn.turnId);
    } catch (error) {
      setConnectionError(error instanceof Error ? error.message : "取消失败");
    }
  }

  function onComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void submit();
    }
  }

  const connectionLabel = connection === "ready"
    ? "会话就绪"
    : connection === "connecting"
      ? "正在连接"
      : connection === "disconnected"
        ? "连接中断"
        : connection === "error"
          ? "连接失败"
          : "读取角色";

  return (
    <section className="conversation-debug-page">
      <PageHeader
        title="对话调试"
        description="通过真实 Session 与 Turn 链路评测角色回复，并把每轮用量、阶段和工具记录放在同一工作区。"
        status={connectionLabel}
        ready={connection === "ready"}
        action={
          <Button
            variant="soft"
            disabled={!selectedCharacterId || connection === "connecting"}
            onClick={() => void connect(selectedCharacterId, true)}
          >
            <PlusIcon /> 新建会话
          </Button>
        }
      />

      {catalogError ? (
        <InlineNotice tone="error" title="角色读取失败">{catalogError}</InlineNotice>
      ) : null}

      <div className="debug-workspace">
        <aside className="debug-session-panel" aria-label="调试会话设置">
          <header className="debug-panel-heading">
            <div>
              <span className="debug-eyebrow">SESSION</span>
              <h2>会话设置</h2>
            </div>
            <span className={`debug-connection-dot ${connection}`} aria-hidden="true" />
          </header>

          {!catalog ? (
            <div className="debug-panel-loading">正在读取角色…</div>
          ) : catalog.characters.length === 0 ? (
            <div className="debug-empty-action">
              <EmptyState title="还没有角色" description="先创建角色，再开始对话调试。" />
              <Button onClick={onOpenCharacters}>前往角色页</Button>
            </div>
          ) : (
            <div className="debug-session-body">
              <label className="debug-field-label" htmlFor="debug-character">调试角色</label>
              <Select.Root
                value={selectedCharacterId}
                onValueChange={(value) => {
                  setSelectedCharacterId(value);
                  void connect(value, false);
                }}
              >
                <Select.Trigger id="debug-character" aria-label="调试角色" />
                <Select.Content>
                  {catalog.characters.map((character) => (
                    <Select.Item key={character.characterId} value={character.characterId}>{character.name}</Select.Item>
                  ))}
                </Select.Content>
              </Select.Root>

              <div className="debug-session-status">
                <div>
                  <span>连接状态</span>
                  <strong>{connectionLabel}</strong>
                </div>
                {connection === "disconnected" || connection === "error" ? (
                  <Button size="1" variant="soft" onClick={() => void connect(selectedCharacterId, false)}>
                    <ReloadIcon /> 重连
                  </Button>
                ) : null}
              </div>

              {connectionError ? <p className="debug-connection-error">{connectionError}</p> : null}

              <dl className="debug-session-facts">
                <div><dt>角色</dt><dd>{selectedCharacter?.name || "未选择"}</dd></div>
                <div><dt>交互</dt><dd>私人 · 文字</dd></div>
                <div><dt>上下文</dt><dd>读取真实记忆</dd></div>
                <div><dt>学习</dt><dd>已隔离</dd></div>
                <div><dt>Conversation</dt><dd title={opened?.conversationId}>{opened ? opened.conversationId.slice(0, 8) : "尚未建立"}</dd></div>
              </dl>

              <InlineNotice title="评测边界">
                回复会读取当前角色、用户记忆和知识，但本会话不会触发长期记忆抽取或知识写入。
              </InlineNotice>
            </div>
          )}
        </aside>

        <section className="debug-chat-panel" aria-label="调试对话">
          <header className="debug-panel-heading debug-chat-heading">
            <div>
              <span className="debug-eyebrow">TRANSCRIPT</span>
              <h2>{selectedCharacter?.name || "角色对话"}</h2>
            </div>
            {activeTurnId ? <Badge color="blue" variant="soft">回复生成中</Badge> : <span className="debug-turn-count">{turns.length} 个 Turn</span>}
          </header>

          <div className="debug-transcript" ref={transcriptRef} aria-live="polite">
            {messages.length === 0 && !activeTurnId ? (
              <div className="debug-chat-empty">
                <strong>从一条真实问题开始</strong>
                <p>你发送的内容会进入独立评测会话，角色回复与正式私人对话使用同一条执行链路。</p>
              </div>
            ) : (
              messages.map((message) => {
                const bubbles = projectMessageBubbles(message);
                if (bubbles.length === 0) return null;
                return (
                  <article key={message.id} className={`debug-message ${message.role}`}>
                    <span>{message.role === "user" ? "你" : selectedCharacter?.name || "角色"}</span>
                    <div className="debug-message-bubbles">
                      {bubbles.map((bubble, index) => bubble.kind === "utterance" ? (
                        <p key={`${message.id}-utterance-${index}`} className="debug-message-bubble" data-expression-kind="utterance">
                          {bubble.text}
                        </p>
                      ) : (
                        <div key={`${message.id}-sticker-${index}`} className="debug-message-bubble sticker" data-expression-kind="sticker">
                          <span>表情包</span>
                          <strong>{bubble.description}</strong>
                        </div>
                      ))}
                    </div>
                    <div className="debug-message-meta">
                      {message.role === "user" && message.messageId ? (
                        <button
                          type="button"
                          className="debug-message-id"
                          title={message.messageId}
                          aria-label={`复制 messageId ${message.messageId}`}
                          onClick={() => void copyMessageId(message.messageId || "")}
                        >
                          <CopyIcon aria-hidden="true" />
                          <code>{copiedMessageId === message.messageId ? "已复制" : message.messageId}</code>
                        </button>
                      ) : null}
                      <time>{new Date(message.createdAtUnixMs).toLocaleTimeString("zh-CN", { hour12: false })}</time>
                    </div>
                  </article>
                );
              })
            )}
            {activeTurnId ? (
              <article className="debug-message assistant pending">
                <span>{selectedCharacter?.name || "角色"}</span>
                <div className="debug-message-bubbles">
                  <p className="debug-message-bubble">{pendingPreview || "正在理解上下文并组织回复…"}</p>
                </div>
                <i aria-hidden="true"><b /><b /><b /></i>
              </article>
            ) : null}
          </div>

          <div className="debug-composer">
            <TextArea
              aria-label="调试消息"
              placeholder={connection === "ready" ? "输入测试消息，Enter 发送，Shift + Enter 换行" : "会话就绪后可以发送消息"}
              value={draft}
              maxLength={4000}
              disabled={connection !== "ready" || Boolean(activeTurnId)}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={onComposerKeyDown}
            />
            <div className="debug-composer-foot">
              <span>{draft.length}/4000</span>
              {activeTurnId ? (
                <Button color="red" variant="soft" disabled={!turns.some((turn) => turn.turnId === activeTurnId)} onClick={() => void cancel()}>
                  <StopIcon /> 取消回复
                </Button>
              ) : (
                <Button disabled={connection !== "ready" || !draft.trim()} onClick={() => void submit()}>
                  <PaperPlaneIcon /> 发送
                </Button>
              )}
            </div>
          </div>
        </section>

        <aside className="debug-inspector-panel" aria-label="Turn 评测明细">
          <header className="debug-panel-heading">
            <div>
              <span className="debug-eyebrow">INSPECTOR</span>
              <h2>Turn 明细</h2>
            </div>
            {selectedTurn ? <Badge variant="soft" color={selectedTurn.state === "failed" ? "red" : selectedTurn.state === "completed" ? "green" : "blue"}>{STATE_LABELS[selectedTurn.state] || selectedTurn.state}</Badge> : null}
          </header>

          {!selectedTurn ? (
            <EmptyState title="暂无 Turn" description="发送消息后，这里会展示阶段、用量和工具执行记录。" />
          ) : (
            <div className="debug-inspector-body">
              <div className="debug-turn-picker" aria-label="选择 Turn">
                {turns.map((turn, index) => {
                  const id = turn.turnId || turn.localId;
                  return (
                    <button key={turn.localId} type="button" className={id === (selectedTurn.turnId || selectedTurn.localId) ? "active" : ""} onClick={() => setSelectedTurnId(id)}>
                      <span>#{index + 1}</span>
                      <small>{STATE_LABELS[turn.state] || turn.state}</small>
                    </button>
                  );
                })}
              </div>

              {selectedTurn.error ? (
                <div className="debug-turn-error"><Cross2Icon /><span>{selectedTurn.error}</span></div>
              ) : null}

              <div className="debug-metric-grid">
                <div><span>耗时</span><strong>{formatDuration(selectedTurn, clockNow)}</strong></div>
                <div><span>输入 Token</span><strong>{selectedUsage.input?.toLocaleString() ?? metricPlaceholder}</strong></div>
                <div><span>输出 Token</span><strong>{selectedUsage.output?.toLocaleString() ?? metricPlaceholder}</strong></div>
                <div><span>缓存命中</span><strong>{selectedUsage.cached?.toLocaleString() ?? metricPlaceholder}</strong></div>
              </div>

              <TurnRuntimeTimeline
                events={selectedTurn.runtime || []}
                state={selectedTurn.runtimeState}
                terminal={selectedTurnTerminal}
                onRetry={selectedTurn.turnId && opened
                  ? () => void loadRuntime(opened.conversationId, selectedTurn.turnId, generationRef.current)
                  : undefined}
              />
            </div>
          )}
        </aside>
      </div>
    </section>
  );
}
