import assert from "node:assert/strict";
import { deriveStagePlaybackState, NEXT_ACTION } from "./stagePlayback.js";

function playback(options) {
  return deriveStagePlaybackState({
    hasNextNode: true,
    lineCount: 1,
    lineIndex: 0,
    typewriterDone: true,
    ...options,
  });
}

assert.equal(
  playback({ lineCount: 3, lineIndex: 0, stageWaiting: true }).nextAction,
  NEXT_ACTION.NEXT_LINE,
  "下一幕等待时，当前幕未读 line 仍应本地推进",
);

assert.equal(
  playback({ lineCount: 3, lineIndex: 2, stageWaiting: true }).nextAction,
  NEXT_ACTION.WAIT_NEXT_NODE,
  "读到最后一条 line 后才等待下一幕",
);

{
  const state = playback({ lineCount: 3, lineIndex: 2, stageWaiting: true });
  assert.equal(state.advanceDisabled, true);
  assert.equal(state.advanceLabel, "准备中");
}

{
  const state = playback({ lineCount: 1, lineIndex: 0, stageWaiting: true, typewriterDone: false });
  assert.equal(state.nextAction, NEXT_ACTION.COMPLETE_TYPEWRITER);
  assert.equal(state.advanceDisabled, false);
  assert.equal(state.advanceLabel, "下一句");
}

assert.equal(
  playback({ lineCount: 1, lineIndex: 0, stageWaiting: false }).nextAction,
  NEXT_ACTION.ADVANCE_NODE,
  "最后一条 line 且下一幕 ready 时应允许跨幕推进",
);

{
  const state = playback({ hasChoices: true, lineCount: 1, lineIndex: 0, stageWaiting: false });
  assert.equal(state.nextAction, NEXT_ACTION.CHOICE_PENDING);
  assert.equal(state.isChoicePending, true);
  assert.equal(state.shouldShowAdvance, false);
  assert.equal(state.advanceDisabled, true);
  assert.equal(state.advanceLabel, "请选择");
}

{
  const state = playback({ hasChoices: true, lineCount: 1, lineIndex: 0, stageWaiting: true });
  assert.equal(state.nextAction, NEXT_ACTION.WAIT_NEXT_NODE);
  assert.equal(state.shouldShowAdvance, true);
  assert.equal(state.advanceDisabled, true);
  assert.equal(state.advanceLabel, "准备中");
}

assert.equal(
  playback({ hasChoices: true, lineCount: 3, lineIndex: 0, stageWaiting: true }).nextAction,
  NEXT_ACTION.NEXT_LINE,
  "有选项但当前幕未读完时，仍应本地推进 line",
);

assert.equal(
  playback({ busy: true, lineCount: 3, lineIndex: 0, stageWaiting: false }).nextAction,
  NEXT_ACTION.NONE,
  "全局忙碌时不执行下一步动作",
);

{
  const state = deriveStagePlaybackState({ lineCount: 0, lineIndex: 7, hasNextNode: false });
  assert.equal(state.lineIndex, 0);
  assert.equal(state.nextAction, NEXT_ACTION.NONE);
  assert.equal(state.shouldShowAdvance, false);
}

console.log("stagePlayback tests passed");
