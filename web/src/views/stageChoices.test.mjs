import assert from "node:assert/strict";
import { choiceDisplayHint, choiceDisplayLabel, choicePlayerText, confirmedChoicePlayerText } from "./stageChoices.js";

const choice = {
  label: "先看例子",
  hint: "用课堂比喻理解",
  text: "亚托莉接下来会用一个完整例子解释 goroutine。"
};

assert.equal(choiceDisplayLabel(choice, 0), "先看例子");
assert.equal(choiceDisplayHint(choice), "用课堂比喻理解");
assert.equal(choicePlayerText(choice, 0), "先看例子");

assert.equal(
  choiceDisplayLabel({ text: "这是一段分支对白，不应该当成按钮。" }, 1),
  "选项 B",
  "缺少 label/hint 时使用稳定 fallback，不把 choice.text 当按钮文案",
);

assert.equal(
  choicePlayerText({ text: "模型回复正文" }, 2),
  "选项 C",
  "用户消息不应优先记录 choice.text",
);

assert.equal(
  confirmedChoicePlayerText(choice, 0, false),
  "",
  "分支尚未真正推进时，不应追加用户选择消息",
);

assert.equal(
  confirmedChoicePlayerText(choice, 0, true),
  "先看例子",
  "分支推进成功后，用户消息才记录选择文案",
);

console.log("stageChoices tests passed");
