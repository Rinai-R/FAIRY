import assert from "node:assert/strict";
import { chapterSnapshotCount, chapterSnapshotItems, chapterSnapshotPosition, hiddenChapterCount, visitedChapterCount } from "./historySnapshots.js";

const nodes = Array.from({ length: 8 }, (_, index) => ({
  id: `lesson-${index + 1}`,
  kind: "lesson",
  title: `第 ${index + 1} 幕`
}));

assert.deepEqual(
  chapterSnapshotItems(nodes).map((item) => item.key),
  nodes.map((node) => node.id),
  "历史详情章节快照不应默认截断章节",
);

assert.deepEqual(
  chapterSnapshotItems(nodes, { limit: 6 }).map((item) => item.number),
  [1, 2, 3, 4, 5, 6],
  "紧凑预览可以显式限制展示数量",
);

assert.equal(hiddenChapterCount(nodes, { limit: 6 }), 2);
assert.equal(hiddenChapterCount(nodes), 0);
assert.equal(chapterSnapshotItems(null).length, 0);

const mixedNodes = [
  { id: "opening", kind: "opening", title: "开场", lines: [{ text: "第一句" }, { text: "第二句" }] },
  { id: "choice-a", kind: "choice", title: "选项 A", summary: "不应作为章节展示" },
  { id: "lesson-1", kind: "lesson", title: "第一幕", choices: [{ label: "继续" }] },
  { id: "line-1", kind: "line", title: "某句台词" },
  { id: "free", kind: "free_discussion", title: "自由讨论" },
  { id: "summary", kind: "summary", title: "总结" },
];

assert.deepEqual(
  chapterSnapshotItems(mixedNodes).map((item) => item.key),
  ["opening", "lesson-1", "summary"],
  "章节快照只展示剧情章节节点，不把选项、自由讨论或单句台词当章节",
);
assert.equal(chapterSnapshotCount(mixedNodes), 3, "章节数量应按剧情章节节点统计");
assert.deepEqual(
  chapterSnapshotPosition(mixedNodes, "summary"),
  { index: 2, number: 3, total: 3, node: mixedNodes[5] },
  "summary 应纳入章节编号",
);
assert.equal(chapterSnapshotPosition(mixedNodes, "choice-a"), null, "选项节点不应拥有章节编号");
assert.equal(chapterSnapshotPosition(mixedNodes, "free"), null, "自由讨论节点不应拥有章节编号");
assert.equal(hiddenChapterCount(mixedNodes, { limit: 2 }), 1);
assert.equal(
  visitedChapterCount(mixedNodes, [
    { node_id: "opening", action: "advance" },
    { node_id: "opening", action: "audio" },
    { node_id: "choice-a", action: "choice" },
    { node_id: "lesson-1", action: "advance" },
  ]),
  2,
  "章节进度只统计进入过的章节节点",
);

console.log("historySnapshots tests passed");
