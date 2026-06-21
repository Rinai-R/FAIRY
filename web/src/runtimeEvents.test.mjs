import assert from "node:assert/strict";
import {
  aggregateRuntimeEventsFromRecords,
  buildLogTimeline,
  createFrontendLog,
  filterLogTimeline,
  normalizeRuntimeEvents,
  recordRuntimeEvents,
  recordRuntimeIssueLogs,
  recordRuntimeTimeline,
  runtimeEventDiagnosticText,
  runtimeEventMeta,
  runtimeLogCopyText,
  runtimeIssueLogs
} from "./runtimeEvents.js";

const runtimePayload = {
  events: [
    {
      id: "event-voice",
      session_id: "session-1",
      level: "error",
      type: "voice.synthesize.failed",
      stage: "voice",
      message: "quota exceeded",
      node_id: "lesson-1",
      provider: "volcengine",
      retry_count: 2,
      duration_ms: 1480,
      created_at: "2026-06-20T08:01:00.000Z"
    },
    {
      id: "event-voice",
      session_id: "session-1",
      level: "error",
      type: "voice.synthesize.failed",
      stage: "voice",
      message: "quota exceeded",
      node_id: "lesson-1",
      provider: "volcengine",
      created_at: "2026-06-20T08:01:00.000Z"
    },
    {
      id: "event-generation",
      session_id: "session-1",
      level: "info",
      type: "generation.created",
      stage: "generation",
      message: "生成任务已创建",
      created_at: "2026-06-20T08:00:00.000Z"
    }
  ]
};

const frontendLog = createFrontendLog("warn", "本地配置保存较慢", new Date("2026-06-20T08:02:00.000Z"));
const normalizedEvents = normalizeRuntimeEvents(runtimePayload);
assert.equal(normalizedEvents.length, 3);
assert.equal(normalizedEvents[0].level, "error");
assert.equal(normalizedEvents[0].node_id, "lesson-1");
assert.equal(normalizedEvents[0].retry_count, 2);
assert.equal(normalizedEvents[0].duration_ms, 1480);

const timeline = buildLogTimeline([frontendLog], runtimePayload);
assert.equal(timeline.length, 3, "重复 runtime event id 应去重");
assert.equal(timeline[0].source, "frontend");
assert.equal(timeline[1].source, "runtime");
assert.equal(timeline[1].message, "quota exceeded");
assert.equal(runtimeEventMeta(timeline[1]), "voice · lesson-1 · volcengine · 耗时 1.48s · 重试 2 次");

const issues = runtimeIssueLogs(timeline);
assert.deepEqual(
  issues.map((item) => `${item.source}:${item.level}:${item.message}`),
  ["runtime:error:quota exceeded", "frontend:warn:本地配置保存较慢"],
  "待处理问题应优先展示 error，再展示 warn，避免后端错误被前端警告压住",
);

assert.deepEqual(
  filterLogTimeline(timeline, { level: "error", query: "耗时 1.48s" }).map((item) => item.id),
  ["runtime:event-voice"],
  "搜索应覆盖 provider / stage / node / duration 等 runtime metadata",
);

assert.deepEqual(
  filterLogTimeline(timeline, { source: "runtime", stage: "voice" }).map((item) => item.id),
  ["runtime:event-voice"],
  "筛选应支持 source 与 stage 组合",
);

const agentRetryPayload = {
  events: [
    {
      id: "event-agent-retry",
      session_id: "session-1",
      level: "warn",
      type: "agent.generate_act.retry",
      stage: "agent",
      message: "Agent 输出不符合合约，正在修正重试。",
      detail: "choices[0].label 必须是短按钮文案，当前 24/16 字",
      node_id: "lesson-1",
      provider: "fairy-agent",
      retry_count: 1,
      duration_ms: 820,
      created_at: "2026-06-20T08:00:45.000Z"
    }
  ]
};
const agentRetryTimeline = buildLogTimeline([], agentRetryPayload);
assert.equal(runtimeEventMeta(agentRetryTimeline[0]), "agent · lesson-1 · fairy-agent · 耗时 820ms · 重试 1 次");
assert.deepEqual(
  runtimeIssueLogs(agentRetryTimeline).map((item) => `${item.type}:${item.level}:${item.message}`),
  ["agent.generate_act.retry:warn:Agent 输出不符合合约，正在修正重试。"],
  "GenerateAct 修正重试应进入最近问题",
);
assert.deepEqual(
  filterLogTimeline(agentRetryTimeline, { level: "warn", query: "短按钮" }).map((item) => item.id),
  ["runtime:event-agent-retry"],
  "搜索应覆盖 GenerateAct retry detail",
);
assert.equal(
  runtimeLogCopyText(agentRetryTimeline[0]),
  [
    "time: 2026-06-20T08:00:45.000Z",
    "source: runtime",
    "level: warn",
    "type: agent.generate_act.retry",
    "stage: agent",
    "node_id: lesson-1",
    "provider: fairy-agent",
    "duration_ms: 820",
    "retry_count: 1",
    "message: Agent 输出不符合合约，正在修正重试。",
    "detail: choices[0].label 必须是短按钮文案，当前 24/16 字",
  ].join("\n"),
);
assert.equal(
  runtimeEventDiagnosticText(agentRetryTimeline[0]),
  "choices[0].label 必须是短按钮文案，当前 24/16 字",
  "runtime detail 应直接作为诊断详情展示",
);

const agentSubstepPayload = {
  events: [
    {
      id: "event-actplan-retry",
      session_id: "session-1",
      level: "warn",
      type: "agent.actplan.retry",
      stage: "agent",
      message: "ActPlan 输出不符合合约，正在修正重试。",
      detail: "act_plan.material_summary 不能为空",
      node_id: "opening",
      provider: "fairy-agent",
      retry_count: 1,
      duration_ms: 31,
      created_at: "2026-06-20T08:00:10.000Z"
    },
    {
      id: "event-rewrite-done",
      session_id: "session-1",
      level: "info",
      type: "agent.rewrite_act.completed",
      stage: "agent",
      message: "RewriteAct 已完成角色口吻改写。",
      node_id: "opening",
      provider: "fairy-agent",
      duration_ms: 46,
      created_at: "2026-06-20T08:00:12.000Z"
    }
  ]
};
const agentSubstepTimeline = buildLogTimeline([], agentSubstepPayload);
assert.deepEqual(
  filterLogTimeline(agentSubstepTimeline, { query: "actplan" }).map((item) => item.id),
  ["runtime:event-actplan-retry"],
  "搜索应支持 ActPlan 子步骤 alias",
);
assert.deepEqual(
  filterLogTimeline(agentSubstepTimeline, { query: "correction" }).map((item) => item.id),
  ["runtime:event-actplan-retry"],
  "搜索 correction 应命中 agent 子步骤 retry",
);
assert.deepEqual(
  filterLogTimeline(agentSubstepTimeline, { query: "rewrite" }).map((item) => item.id),
  ["runtime:event-rewrite-done"],
  "搜索应支持 RewriteAct 子步骤 alias",
);
assert.equal(
  runtimeLogCopyText(agentSubstepTimeline.find((item) => item.id === "runtime:event-actplan-retry")),
  [
    "time: 2026-06-20T08:00:10.000Z",
    "source: runtime",
    "level: warn",
    "type: agent.actplan.retry",
    "stage: agent",
    "node_id: opening",
    "provider: fairy-agent",
    "duration_ms: 31",
    "retry_count: 1",
    "message: ActPlan 输出不符合合约，正在修正重试。",
    "detail: act_plan.material_summary 不能为空",
  ].join("\n"),
  "复制子步骤事件应保留诊断字段",
);

assert.deepEqual(
  recordRuntimeIssueLogs({ events: runtimePayload.events }).map((item) => item.id),
  ["runtime:event-voice"],
  "历史记录详情应能复用 runtime event issue 提取",
);

const historyTimeline = recordRuntimeTimeline({
  events: [
    {
      id: "event-generation",
      level: "info",
      type: "generation.created",
      stage: "generation",
      message: "生成任务已创建",
      created_at: "2026-06-20T08:00:00.000Z"
    },
    {
      id: "event-workflow",
      level: "info",
      type: "workflow.node.completed",
      stage: "workflow",
      message: "lesson-1 已完成",
      node_id: "lesson-1",
      duration_ms: 620,
      created_at: "2026-06-20T08:00:30.000Z"
    },
    {
      id: "event-voice",
      level: "error",
      type: "voice.synthesize.failed",
      stage: "voice",
      message: "quota exceeded",
      node_id: "lesson-1",
      provider: "volcengine",
      retry_count: 2,
      duration_ms: 1480,
      created_at: "2026-06-20T08:01:00.000Z"
    },
    {
      id: "event-voice",
      level: "error",
      type: "voice.synthesize.failed",
      stage: "voice",
      message: "quota exceeded duplicated",
      created_at: "2026-06-20T08:01:00.000Z"
    }
  ]
});

assert.deepEqual(
  historyTimeline.map((item) => item.id),
  ["runtime:event-voice", "runtime:event-workflow", "runtime:event-generation"],
  "历史详情 runtime timeline 应展示完整事件、按时间倒序且按 ID 去重",
);
assert.equal(runtimeEventMeta(historyTimeline[1]), "workflow · lesson-1 · 耗时 620ms");
assert.equal(recordRuntimeTimeline({ events: runtimePayload.events }, 1).length, 1, "历史详情 timeline 应支持显示条数限制");
assert.equal(recordRuntimeTimeline({ events: [] }).length, 0, "无 runtime events 的历史记录不应生成空 timeline");
assert.equal(recordRuntimeTimeline({ events: runtimePayload.events }, 0).length, 2, "非正数 limit 表示不裁剪");

const legacyFailedRecord = {
  session: { id: "legacy-failed" },
  scene: { title: "Syncpool" },
  updated_at: "2026-06-20T12:18:37.805Z",
  generation: {
    status: "failed",
    error: "FAIRY GenerateAct 输出连续不符合合约: node.lines 至少需要 4 条文本框台词，当前 0 条",
    completed_at: "2026-06-20T12:18:37.805Z",
    request: { topic: "Syncpool" }
  },
  workflow: {
    nodes: [
      {
        id: "lesson-2",
        title: "第2幕",
        lines: [
          { text: "第一句", audio_status: "ready" },
          { text: "第二句", audio_status: "error", audio_error: "quota exceeded" }
        ]
      }
    ]
  }
};

const derivedLegacyEvents = recordRuntimeEvents(legacyFailedRecord);
assert.deepEqual(
  derivedLegacyEvents.map((event) => `${event.type}:${event.stage}:${event.level}`),
  ["generation.failed:generation:error", "voice.synthesize.failed:voice:error"],
  "旧失败记录没有 events 时也应派生为后端观测事件",
);
assert.equal(derivedLegacyEvents[0].detail, "Syncpool");
assert.deepEqual(
  recordRuntimeIssueLogs(legacyFailedRecord).map((item) => item.message),
  [
    "FAIRY GenerateAct 输出连续不符合合约: node.lines 至少需要 4 条文本框台词，当前 0 条",
    "第2幕 第 2 句语音生成失败：quota exceeded",
  ],
  "历史详情最近问题应包含旧 generation.error 和 workflow 语音错误",
);

const aggregatedEvents = aggregateRuntimeEventsFromRecords([
  {
    session: { id: "background-generation" },
    events: [
      {
        id: "event-background-failure",
        level: "error",
        type: "generation.failed",
        stage: "generation",
        message: "后台生成失败",
        created_at: "2026-06-20T08:05:00.000Z"
      },
      {
        id: "event-voice",
        level: "error",
        type: "voice.synthesize.failed",
        stage: "voice",
        message: "record copy should be deduped behind active event",
        created_at: "2026-06-20T08:01:00.000Z"
      }
    ]
  }
], runtimePayload.events, 2);

assert.deepEqual(
  aggregatedEvents.map((event) => event.id),
  ["event-background-failure", "event-voice"],
  "制作日志应聚合历史记录中的后台 runtime events，而不是只依赖 active session",
);
assert.equal(
  aggregatedEvents.find((event) => event.id === "event-voice").message,
  "quota exceeded",
  "active session 拉取到的新事件应优先于历史列表中的旧拷贝",
);

const aggregatedLegacyEvents = aggregateRuntimeEventsFromRecords([legacyFailedRecord], [], 5);
assert.deepEqual(
  aggregatedLegacyEvents.map((event) => event.id),
  [
    "derived:legacy-failed:generation.failed",
    "derived:legacy-failed:lesson-2:line-2:voice.synthesize.failed",
  ],
  "制作日志应聚合旧失败记录派生的后端错误，并在同时间戳时优先显示根因",
);

assert.equal(
  runtimeLogCopyText(timeline[1]),
  [
    "time: 2026-06-20T08:01:00.000Z",
    "source: runtime",
    "level: error",
    "type: voice.synthesize.failed",
    "stage: voice",
    "node_id: lesson-1",
    "provider: volcengine",
    "duration_ms: 1480",
    "retry_count: 2",
    "message: quota exceeded",
  ].join("\n"),
);

assert.equal(normalizeRuntimeEvents({ events: [{ message: "" }, null] }).length, 0);
assert.equal(normalizeRuntimeEvents({ events: [{ message: "bad retry", retry_count: -1 }] })[0].retry_count, 0);
assert.equal(normalizeRuntimeEvents({ events: [{ message: "bad duration", duration_ms: -1 }] })[0].duration_ms, 0);
assert.equal(runtimeEventMeta(normalizeRuntimeEvents({ events: [{ message: "old event", stage: "generation" }] })[0]), "generation");

console.log("runtimeEvents tests passed");
