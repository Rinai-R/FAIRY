import assert from "node:assert/strict";
import { buildBacklogItems } from "./stageBacklog.js";

const nodes = [
  {
    id: "opening",
    speaker: "亚托莉",
    lines: [
      { speaker: "亚托莉", text: "今天我们一起探索Go调度器的奥秘吧！" },
      { speaker: "亚托莉", text: "首先，想象一下教室里的四人小组作业~" },
      { speaker: "亚托莉", text: "G就像待办事项卡片，记录要完成的任务。" },
      { speaker: "亚托莉", text: "M是组员，真正动手执行任务的人。" },
      { speaker: "亚托莉", text: "P是小组本身，持有任务清单和共用文具。" },
      { speaker: "亚托莉", text: "那么，下面哪个比喻最像Goroutine呢？" },
    ],
  },
  {
    id: "lesson-1",
    speaker: "亚托莉",
    lines: [
      { speaker: "亚托莉", text: "Go程序启动的第一件事，就是创建m0和g0这两个特殊成员。" },
    ],
  },
  {
    id: "lesson-2",
    speaker: "亚托莉",
    lines: [
      { speaker: "亚托莉", text: "接下来，P会把可运行的G交给M执行。" },
    ],
  },
];

{
  const items = buildBacklogItems(
    [{ node_id: "opening", action: "enter" }],
    nodes,
    "opening",
    5,
    "亚托莉",
  );

  assert.deepEqual(
    items.map((item) => item.text),
    [
      "今天我们一起探索Go调度器的奥秘吧！",
      "首先，想象一下教室里的四人小组作业~",
      "G就像待办事项卡片，记录要完成的任务。",
      "M是组员，真正动手执行任务的人。",
      "P是小组本身，持有任务清单和共用文具。",
      "那么，下面哪个比喻最像Goroutine呢？",
    ],
    "backlog 应按剧情 line 的原始顺序显示，不应反转或插入 opening_message",
  );
  assert.equal(items.at(-1).active, true);
}

{
  const items = buildBacklogItems(
    [{ node_id: "opening", action: "enter" }],
    nodes,
    "opening",
    2,
    "亚托莉",
  );

  assert.deepEqual(
    items.map((item) => item.text),
    [
      "今天我们一起探索Go调度器的奥秘吧！",
      "首先，想象一下教室里的四人小组作业~",
      "G就像待办事项卡片，记录要完成的任务。",
    ],
    "当前幕未读到的 line 不应提前出现在回忆记录里",
  );
}

{
  const items = buildBacklogItems(
    [
      { node_id: "opening", action: "enter" },
      { node_id: "lesson-1", action: "choice", choice_label: "模块优先" },
    ],
    nodes,
    "lesson-1",
    0,
    "亚托莉",
  );

  assert.equal(items[6].kind, "user");
  assert.equal(items[6].text, "选择：模块优先");
  assert.equal(items[7].text, "Go程序启动的第一件事，就是创建m0和g0这两个特殊成员。");
}

{
  const items = buildBacklogItems(
    [
      { node_id: "opening", action: "enter" },
      { node_id: "lesson-1", action: "advance" },
      { node_id: "lesson-2", action: "advance" },
      { node_id: "lesson-1", action: "replay" },
    ],
    nodes,
    "lesson-1",
    0,
    "亚托莉",
  );

  assert.deepEqual(
    items.map((item) => item.text),
    [
      "今天我们一起探索Go调度器的奥秘吧！",
      "首先，想象一下教室里的四人小组作业~",
      "G就像待办事项卡片，记录要完成的任务。",
      "M是组员，真正动手执行任务的人。",
      "P是小组本身，持有任务清单和共用文具。",
      "那么，下面哪个比喻最像Goroutine呢？",
      "Go程序启动的第一件事，就是创建m0和g0这两个特殊成员。",
    ],
    "跳回中途后，回忆记录应裁回 replay 目标，不应继续显示原先已经走过的后续节点",
  );
}

{
  const items = buildBacklogItems(
    [
      { node_id: "opening", action: "enter" },
      { node_id: "lesson-1", action: "advance" },
      { node_id: "lesson-2", action: "advance" },
      { node_id: "lesson-1", action: "replay" },
      { node_id: "lesson-2", action: "advance" },
    ],
    nodes,
    "lesson-2",
    0,
    "亚托莉",
  );

  assert.deepEqual(
    items.map((item) => item.text),
    [
      "今天我们一起探索Go调度器的奥秘吧！",
      "首先，想象一下教室里的四人小组作业~",
      "G就像待办事项卡片，记录要完成的任务。",
      "M是组员，真正动手执行任务的人。",
      "P是小组本身，持有任务清单和共用文具。",
      "那么，下面哪个比喻最像Goroutine呢？",
      "Go程序启动的第一件事，就是创建m0和g0这两个特殊成员。",
      "接下来，P会把可运行的G交给M执行。",
    ],
    "从 replay 目标继续推进时，后续节点只显示一次",
  );
}

console.log("stageBacklog tests passed");
