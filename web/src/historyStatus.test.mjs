import assert from "node:assert/strict";
import {
  historyStatusDescription,
  historyStatusLabel,
  recordPrimaryHistoryTime,
  recordGenerationStatus,
  recordHasActiveGeneration,
  recordPlayable,
  recordReadinessTimes,
  workflowArchiveComplete
} from "./historyStatus.js";

function readyNode(id, kind = "lesson", extra = {}) {
  return {
    id,
    kind,
    status: "ready",
    voice_status: "ready",
    ready_at: extra.ready_at || "",
    lines: [{ speaker: "亚托莉", text: `${id} 正文`, audio: { url: `/audio/${id}.mp3` }, audio_status: "ready" }],
    ...extra
  };
}

function record({ generationStatus = "", nodes = [], workflow = {}, sceneID = "lesson", sessionID = "lesson:test", error = "", updatedAt = "2026-06-20T08:00:00.000Z" } = {}) {
  return {
    session: { id: sessionID },
    scene: { id: sceneID },
    generation: { status: generationStatus, error },
    workflow: {
      current_node_id: nodes[0]?.id || "",
      nodes,
      history: [],
      ...workflow
    },
    updated_at: updatedAt
  };
}

const generatingOnly = record({
  generationStatus: "preparing",
  nodes: []
});
assert.equal(recordGenerationStatus(generatingOnly), "generating");
assert.equal(recordPlayable(generatingOnly), false);
assert.equal(recordHasActiveGeneration(generatingOnly), true);

const playablePreparing = record({
  generationStatus: "preparing",
  nodes: [readyNode("opening", "opening", { next_node_id: "lesson-1", ready_at: "2026-06-20T08:01:00.000Z" })],
  workflow: { preparing: true, pending_node_id: "lesson-1" }
});
assert.equal(recordGenerationStatus(playablePreparing), "playable");
assert.equal(recordPlayable(playablePreparing), true);
assert.equal(recordHasActiveGeneration(playablePreparing), true);
assert.equal(historyStatusLabel("playable"), "可演出");
assert.match(historyStatusDescription("playable"), /先进入演出/);
assert.deepEqual(recordReadinessTimes(playablePreparing), {
  playable_at: "2026-06-20T08:01:00.000Z",
  completed_at: "",
  saved_at: "2026-06-20T08:00:00.000Z"
});
assert.deepEqual(recordPrimaryHistoryTime(playablePreparing), {
  label: "可演出",
  value: "2026-06-20T08:01:00.000Z"
});

const playableReadyButIncomplete = record({
  generationStatus: "ready",
  nodes: [readyNode("opening", "opening", { next_node_id: "lesson-1" })]
});
assert.equal(recordGenerationStatus(playableReadyButIncomplete), "playable");
assert.equal(workflowArchiveComplete(playableReadyButIncomplete.workflow), false);

const pendingNodeWithLegacyLine = record({
  generationStatus: "preparing",
  nodes: [{
    id: "opening",
    kind: "opening",
    status: "pending",
    voice_status: "pending",
    line: "这句旧字段不能让 pending 节点变成可演出。"
  }],
  workflow: { preparing: true, pending_node_id: "opening" }
});
assert.equal(recordPlayable(pendingNodeWithLegacyLine), false, "pending 节点即使带旧 line 字段也不能进入演出");
assert.equal(recordGenerationStatus(pendingNodeWithLegacyLine), "generating");

const pendingChoiceNode = record({
  generationStatus: "preparing",
  nodes: [{
    id: "opening",
    kind: "opening",
    status: "pending",
    voice_status: "pending",
    choices: [{ id: "example", label: "先看例子" }]
  }],
  workflow: { preparing: true, pending_node_id: "opening" }
});
assert.equal(recordPlayable(pendingChoiceNode), false, "pending 节点即使带 choices 也不能进入演出");
assert.equal(recordGenerationStatus(pendingChoiceNode), "generating");

const completed = record({
  generationStatus: "ready",
  nodes: [
    readyNode("opening", "opening", { next_node_id: "lesson-1", ready_at: "2026-06-20T08:01:00.000Z" }),
    readyNode("lesson-1", "lesson", { next_node_id: "free-discussion", ready_at: "2026-06-20T08:03:00.000Z" }),
    readyNode("free-discussion", "free_discussion", { free_discussion: true, ready_at: "2026-06-20T08:04:00.000Z" })
  ]
});
assert.equal(workflowArchiveComplete(completed.workflow), true);
assert.equal(recordGenerationStatus(completed), "completed");
assert.equal(historyStatusLabel("completed"), "已完成");
assert.deepEqual(recordReadinessTimes(completed), {
  playable_at: "2026-06-20T08:01:00.000Z",
  completed_at: "2026-06-20T08:04:00.000Z",
  saved_at: "2026-06-20T08:00:00.000Z"
});
assert.deepEqual(recordPrimaryHistoryTime(completed), {
  label: "完成",
  value: "2026-06-20T08:04:00.000Z"
});

const failed = record({
  generationStatus: "ready",
  nodes: [
    readyNode("opening", "opening", { next_node_id: "lesson-1" }),
    { id: "lesson-1", kind: "lesson", status: "error", voice_status: "error", prepare_error: "agent provider down" }
  ]
});
assert.equal(recordGenerationStatus(failed), "failed");

const draftPlayableGuard = record({
  generationStatus: "",
  sceneID: "lesson-draft",
  nodes: [readyNode("opening", "opening")]
});
assert.equal(recordPlayable(draftPlayableGuard), false);
assert.equal(recordGenerationStatus(draftPlayableGuard), "draft");
assert.deepEqual(recordReadinessTimes(draftPlayableGuard), {
  playable_at: "",
  completed_at: "",
  saved_at: "2026-06-20T08:00:00.000Z"
});
assert.deepEqual(recordPrimaryHistoryTime(draftPlayableGuard), {
  label: "保存",
  value: "2026-06-20T08:00:00.000Z"
});

const playableFromLegacyHistory = record({
  generationStatus: "preparing",
  nodes: [readyNode("opening", "opening", { ready_at: "", next_node_id: "lesson-1" })],
  workflow: {
    preparing: true,
    pending_node_id: "lesson-1",
    history: [{ node_id: "opening", occurred_at: "2026-06-20T08:02:00.000Z" }]
  }
});
assert.equal(recordReadinessTimes(playableFromLegacyHistory).playable_at, "2026-06-20T08:02:00.000Z");

assert.deepEqual(recordPrimaryHistoryTime(generatingOnly), {
  label: "更新",
  value: "2026-06-20T08:00:00.000Z"
});

const records = [generatingOnly, playablePreparing, completed, failed];
assert.deepEqual(
  records.filter((item) => recordGenerationStatus(item) === "playable").map((item) => item.session.id),
  ["lesson:test"],
  "可演出筛选应只命中可演出未完成记录",
);
assert.equal(
  records.filter((item) => recordGenerationStatus(item) === "completed").length,
  1,
  "已完成筛选应只命中完整闭合记录",
);

console.log("historyStatus tests passed");
