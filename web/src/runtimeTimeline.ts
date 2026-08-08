export type RuntimeEvent = {
  sequence: number;
  eventType: string;
  state?: string;
  code?: string;
  metadata: Record<string, unknown>;
  createdAtUnixMs: number;
};

export type RuntimeTimelineMode = "summary" | "raw";

export type RuntimeDisplayEvent = RuntimeEvent & {
  sourceCount: number;
};

export type RuntimeToolSource = {
  title: string;
  url: string;
  snippet: string;
  rank: number | null;
};

export type RuntimeToolKnowledge = {
  id: string;
  topic: string;
  statement: string;
  sources: RuntimeToolSource[];
};

export type RuntimeToolPersonalMemory = {
  id: string;
  kind: string;
  content: string;
};

export type RuntimeToolSocialMemory = {
  id: string;
  kind: string;
  situation: string;
  content: string;
};

export type RuntimeToolContext = {
  personalMemories: RuntimeToolPersonalMemory[];
  knowledge: RuntimeToolKnowledge[];
  socialMemories: RuntimeToolSocialMemory[];
  semanticStatus: string;
};

export type RuntimeToolDetail = {
  version: "v1";
  query: string;
  status: string;
  result: RuntimeToolContext | null;
  mergedContext: RuntimeToolContext | null;
};

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

function finiteNumber(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function runtimeRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function runtimeString(value: unknown) {
  return typeof value === "string" ? value : "";
}

function runtimeRecords(value: unknown) {
  return Array.isArray(value) ? value.map(runtimeRecord).filter((item): item is Record<string, unknown> => Boolean(item)) : [];
}

function runtimeToolContext(value: unknown): RuntimeToolContext | null {
  const raw = runtimeRecord(value);
  if (!raw) return null;
  const social = runtimeRecord(raw.socialMemories);
  return {
    personalMemories: runtimeRecords(raw.personalMemories).map((item) => ({
      id: runtimeString(item.id),
      kind: runtimeString(item.kind),
      content: runtimeString(item.content),
    })),
    knowledge: runtimeRecords(raw.knowledge).map((item) => ({
      id: runtimeString(item.id),
      topic: runtimeString(item.topic),
      statement: runtimeString(item.statement),
      sources: runtimeRecords(item.sources).map((source) => ({
        title: runtimeString(source.title),
        url: runtimeString(source.url),
        snippet: runtimeString(source.snippet),
        rank: finiteNumber(source.rank),
      })),
    })),
    socialMemories: runtimeRecords(social?.entries).map((item) => ({
      id: runtimeString(item.id),
      kind: runtimeString(item.kind),
      situation: runtimeString(item.situation),
      content: runtimeString(item.content),
    })),
    semanticStatus: runtimeString(raw.semanticStatus),
  };
}

export function runtimeToolDetail(event: RuntimeEvent): RuntimeToolDetail | null {
  if (event.eventType !== "tool") return null;
  const detail = runtimeRecord(event.metadata?.detail);
  if (!detail || detail.version !== "v1") return null;
  const argumentsValue = runtimeRecord(detail.arguments);
  const receipt = runtimeRecord(detail.receipt);
  return {
    version: "v1",
    query: runtimeString(argumentsValue?.query),
    status: runtimeString(receipt?.status) || runtimeString(event.metadata?.status),
    result: runtimeToolContext(detail.result),
    mergedContext: runtimeToolContext(detail.mergedContext),
  };
}

function mergeEvents(previous: RuntimeDisplayEvent, current: RuntimeEvent): RuntimeDisplayEvent {
  return {
    ...previous,
    state: current.state ?? previous.state,
    code: current.code ?? previous.code,
    metadata: { ...previous.metadata, ...(current.metadata || {}) },
    createdAtUnixMs: current.createdAtUnixMs,
    sourceCount: previous.sourceCount + 1,
  };
}

function deliveryIdentity(event: RuntimeEvent) {
  if (event.eventType !== "beat_delivery") return null;
  const kind = typeof event.metadata?.kind === "string" ? event.metadata.kind : null;
  const chainIndex = finiteNumber(event.metadata?.chainIndex);
  const playIndex = finiteNumber(event.metadata?.playIndex);
  if (!kind || chainIndex === null || playIndex === null) return null;
  return `${kind}:${chainIndex}:${playIndex}`;
}

export function projectRuntimeTimeline(events: RuntimeEvent[], mode: RuntimeTimelineMode): RuntimeDisplayEvent[] {
  if (mode === "raw") {
    return events.map((event) => ({ ...event, metadata: { ...(event.metadata || {}) }, sourceCount: 1 }));
  }

  const projected: RuntimeDisplayEvent[] = [];
  for (const event of events) {
    if (event.eventType === "context_window") continue;

    const previous = projected.at(-1);
    if (event.eventType === "model" && previous?.eventType === "model") {
      projected[projected.length - 1] = mergeEvents(previous, event);
      continue;
    }

    const identity = deliveryIdentity(event);
    if (identity && previous?.eventType === "beat_delivery" && deliveryIdentity(previous) === identity) {
      projected[projected.length - 1] = mergeEvents(previous, event);
      continue;
    }

    projected.push({ ...event, metadata: { ...(event.metadata || {}) }, sourceCount: 1 });
  }
  return projected;
}

export type RuntimeUsageTotals = {
  input: number | null;
  output: number | null;
  cached: number | null;
};

function runtimeUsageTotals(value: unknown): RuntimeUsageTotals {
  if (!Array.isArray(value)) return { input: null, output: null, cached: null };
  let input = 0;
  let output = 0;
  let cached = 0;
  let hasInput = false;
  let hasOutput = false;
  let hasCached = false;
  for (const item of value) {
    if (!item || typeof item !== "object") continue;
    const usage = "usage" in item && item.usage && typeof item.usage === "object"
      ? item.usage as Record<string, unknown>
      : null;
    if (!usage) continue;
    const inputTokens = finiteNumber(usage.inputTokens);
    const outputTokens = finiteNumber(usage.outputTokens);
    if (inputTokens !== null) {
      input += inputTokens;
      hasInput = true;
    }
    if (outputTokens !== null) {
      output += outputTokens;
      hasOutput = true;
    }
    const cachedInput = usage.cachedInputTokens;
    if (cachedInput && typeof cachedInput === "object" && "status" in cachedInput && cachedInput.status === "observed") {
      const cachedTokens = "tokens" in cachedInput ? finiteNumber(cachedInput.tokens) : null;
      if (cachedTokens !== null) {
        cached += cachedTokens;
        hasCached = true;
      }
    }
  }
  return {
    input: hasInput ? input : null,
    output: hasOutput ? output : null,
    cached: hasCached ? cached : null,
  };
}

export function runtimeModelUsageTotals(events: RuntimeEvent[]): RuntimeUsageTotals {
  let input = 0;
  let output = 0;
  let cached = 0;
  let hasInput = false;
  let hasOutput = false;
  let hasCached = false;

  for (const event of events) {
    if (event.eventType !== "model") continue;
    const usage = runtimeUsageTotals(event.metadata?.usage);
    if (usage.input !== null) {
      input += usage.input;
      hasInput = true;
    }
    if (usage.output !== null) {
      output += usage.output;
      hasOutput = true;
    }
    if (usage.cached !== null) {
      cached += usage.cached;
      hasCached = true;
    }
  }

  return {
    input: hasInput ? input : null,
    output: hasOutput ? output : null,
    cached: hasCached ? cached : null,
  };
}

function formatInteger(value: number) {
  return value.toLocaleString("zh-CN", { maximumFractionDigits: 0 });
}

function modelSummary(metadata: Record<string, unknown>) {
  const details: string[] = [];
  const completedMs = finiteNumber(metadata.completedMs);
  if (completedMs !== null) details.push(`完成 ${formatInteger(completedMs)} ms`);
  const usage = runtimeUsageTotals(metadata.usage);
  if (usage.input !== null) details.push(`输入 ${formatInteger(usage.input)} Token`);
  if (usage.output !== null) details.push(`输出 ${formatInteger(usage.output)} Token`);
  if (usage.cached !== null) details.push(`缓存命中 ${formatInteger(usage.cached)} Token`);
  if (details.length > 0) return details.join("，");
  const phase = typeof metadata.phase === "string" ? metadata.phase : "";
  return phase ? `模型阶段 ${phase} 已记录` : "模型调用状态已记录";
}

function deliverySummary(metadata: Record<string, unknown>) {
  const chainIndex = finiteNumber(metadata.chainIndex);
  const segment = chainIndex === null ? "回复" : `第 ${formatInteger(chainIndex + 1)} 段`;
  const status = typeof metadata.status === "string" ? metadata.status : "";
  if (status === "published") return `${segment}已发布`;
  if (status === "planned") return `${segment}等待发布`;
  if (status === "failed") return `${segment}发布失败`;
  if (status === "interrupted" || status === "aborted") return `${segment}发布中断`;
  return status ? `${segment}交付状态：${status}` : `${segment}交付状态已记录`;
}

export function runtimeEventSummary(event: RuntimeEvent) {
  const metadata = event.metadata || {};
  if (event.eventType === "transition") {
    return `进入${STATE_LABELS[event.state || "unknown"] || event.state || "未知阶段"}`;
  }
  if (event.eventType === "gather") return "已完成记忆与知识召回准备";
  if (event.eventType === "tool") {
    return [metadata.tool, metadata.phase, metadata.status].filter(Boolean).join(" · ") || "工具状态已记录";
  }
  if (event.eventType === "model") return modelSummary(metadata);
  if (event.eventType === "prompt") {
    const personal = finiteNumber(metadata.retrievedPersonalCount);
    const knowledge = finiteNumber(metadata.retrievedKnowledgeCount);
    const parts = [
      personal === null ? null : `个人记忆 ${formatInteger(personal)}`,
      knowledge === null ? null : `知识 ${formatInteger(knowledge)}`,
    ].filter((part): part is string => Boolean(part));
    return parts.length > 0 ? parts.join("，") : "已组装模型上下文";
  }
  if (event.eventType === "continuation") {
    if (metadata.incremental === true) return "已续接增量上下文";
    if (metadata.incremental === false) return "已准备完整上下文";
    return "已准备模型上下文";
  }
  if (event.eventType === "compile") {
    const chainCount = finiteNumber(metadata.chainCount);
    const status = typeof metadata.status === "string" ? metadata.status : "";
    if (status === "failed") return `回复编译失败${metadata.errorCode ? `：${metadata.errorCode}` : ""}`;
    return chainCount === null ? "回复已编译" : `已编译 ${formatInteger(chainCount)} 个表达段`;
  }
  if (event.eventType === "beat_delivery") return deliverySummary(metadata);
  if (event.eventType === "terminal") {
    const status = typeof metadata.status === "string" ? metadata.status : event.state;
    return `Turn ${STATE_LABELS[status || "unknown"] || status || "状态已记录"}`;
  }
  if (event.eventType === "context_window") {
    const windowNumber = finiteNumber(metadata.windowNumber);
    const observed = finiteNumber(metadata.observedPrefillTokens);
    const estimated = finiteNumber(metadata.estimatedPrefillTokens);
    const details: string[] = [];
    if (windowNumber !== null) details.push(`窗口 ${formatInteger(windowNumber)}`);
    if (observed !== null) details.push(`观测输入 ${formatInteger(observed)} Token`);
    else if (estimated !== null) details.push(`估算输入 ${formatInteger(estimated)} Token`);
    return details.length > 0 ? details.join("，") : "上下文窗口状态已记录";
  }
  if (event.eventType === "context_compaction") {
    const details = [
      typeof metadata.layer === "string" ? `层级 ${metadata.layer}` : null,
      typeof metadata.trigger === "string" ? `触发 ${metadata.trigger}` : null,
      finiteNumber(metadata.releasedTokens) === null ? null : `释放 ${formatInteger(metadata.releasedTokens as number)} Token`,
    ].filter((part): part is string => Boolean(part));
    return details.length > 0 ? details.join("，") : "上下文压缩状态已记录";
  }
  return event.code || "运行状态已记录";
}
