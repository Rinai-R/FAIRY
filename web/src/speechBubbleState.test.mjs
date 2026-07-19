import assert from "node:assert/strict";
import test from "node:test";

import {
  SPEECH_BUBBLE_FADE_AFTER_MS,
  createSpeechBubbleState,
  reduceSpeechBubbleState,
  shouldClearFadedSpeechBubble,
  speechBubbleSurface,
} from "./speechBubbleState.mjs";

test("speech bubble sets target and fades after dwell", () => {
  let state = createSpeechBubbleState();
  state = reduceSpeechBubbleState(state, { type: "set_target", target: "你好呀" });
  assert.equal(state.target, "你好呀");
  assert.equal(state.fading, false);

  const started = 1_000;
  state = reduceSpeechBubbleState(state, { type: "start_fade", at: started });
  assert.equal(state.fading, true);
  assert.equal(shouldClearFadedSpeechBubble(state, started + SPEECH_BUBBLE_FADE_AFTER_MS - 1), false);
  assert.equal(shouldClearFadedSpeechBubble(state, started + SPEECH_BUBBLE_FADE_AFTER_MS), true);

  state = reduceSpeechBubbleState(state, { type: "clear" });
  assert.equal(state.target, "");
});

test("empty target clears bubble", () => {
  let state = reduceSpeechBubbleState(createSpeechBubbleState(), {
    type: "set_target",
    target: "临时",
  });
  state = reduceSpeechBubbleState(state, { type: "set_target", target: "" });
  assert.deepEqual(state, createSpeechBubbleState());
});

test("waiting surface never shares the bubble with reply text", () => {
  assert.deepEqual(speechBubbleSurface("", true), {
    mode: "waiting",
    showText: false,
    showWaiting: true,
  });
  assert.deepEqual(speechBubbleSurface("旧台词还在", true), {
    mode: "text",
    showText: true,
    showWaiting: false,
  });
  assert.deepEqual(speechBubbleSurface("你好", false), {
    mode: "text",
    showText: true,
    showWaiting: false,
  });
  assert.deepEqual(speechBubbleSurface("", false), {
    mode: "hidden",
    showText: false,
    showWaiting: false,
  });
});
