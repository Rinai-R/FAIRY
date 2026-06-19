import assert from "node:assert/strict";
import { stageWorkflowWaiting } from "./workflowReadiness.js";

function readyNode(id, kind = "lesson", extra = {}) {
  return {
    id,
    kind,
    status: "ready",
    voice_status: "ready",
    ...extra,
  };
}

assert.equal(
  stageWorkflowWaiting({
    current_node_id: "opening",
    nodes: [
      readyNode("opening", "opening", { next_node_id: "lesson-1" }),
      readyNode("lesson-1"),
    ],
  }),
  false,
  "无选项节点只要主线下一幕 ready 就不等待",
);

assert.equal(
  stageWorkflowWaiting({
    current_node_id: "opening",
    nodes: [
      readyNode("opening", "opening", {
        next_node_id: "lesson-1",
        choices: [{ id: "example", target_node_id: "opening-choice-example" }],
      }),
      readyNode("lesson-1"),
    ],
  }),
  true,
  "主线 ready 但 choice branch 缺失时仍等待",
);

assert.equal(
  stageWorkflowWaiting({
    current_node_id: "opening",
    nodes: [
      readyNode("opening", "opening", {
        next_node_id: "lesson-1",
        choices: [
          { id: "example", target_node_id: "opening-choice-example" },
          { id: "term", target_node_id: "opening-choice-term" },
        ],
      }),
      readyNode("lesson-1"),
      readyNode("opening-choice-example", "choice"),
      readyNode("opening-choice-term", "choice"),
    ],
  }),
  false,
  "所有直接分支 ready 后才解除等待",
);

assert.equal(
  stageWorkflowWaiting({
    current_node_id: "opening",
    nodes: [
      readyNode("opening", "opening", {
        next_node_id: "lesson-1",
        choices: [{ id: "example", target_node_id: "opening-choice-example" }],
      }),
      readyNode("lesson-1"),
      readyNode("opening-choice-example", "choice", { status: "error", voice_status: "error" }),
    ],
  }),
  true,
  "分支失败不能被视为完成",
);

console.log("workflowReadiness tests passed");
