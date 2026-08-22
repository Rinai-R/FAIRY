import assert from "node:assert/strict";
import test from "node:test";

import {
  createTypewriterState,
  flushTypewriter,
  holdBackMatchingAssistant,
  isTypewriterCaughtUp,
  setTypewriterTarget,
  tickTypewriter,
  typewriterPartialActive,
} from "./typewriter.mjs";

test("tickTypewriter advances by Unicode code points", () => {
  let state = setTypewriterTarget(createTypewriterState(), "你好世界");
  assert.equal(state.visible, "");
  state = tickTypewriter(state, 2);
  assert.equal(state.visible, "你好");
  state = tickTypewriter(state, 2);
  assert.equal(state.visible, "你好世界");
  assert.equal(isTypewriterCaughtUp(state), true);
  assert.equal(tickTypewriter(state, 3), state);
});

test("setTypewriterTarget keeps shared prefix when target grows", () => {
  let state = setTypewriterTarget(createTypewriterState(), "嗯，我");
  state = tickTypewriter(state, 3);
  assert.equal(state.visible, "嗯，我");
  state = setTypewriterTarget(state, "嗯，我懂。");
  assert.equal(state.visible, "嗯，我");
  state = tickTypewriter(state, 2);
  assert.equal(state.visible, "嗯，我懂。");
});

test("setTypewriterTarget trims visible to shared prefix on divergence", () => {
  let state = setTypewriterTarget(createTypewriterState(), "abcdef");
  state = tickTypewriter(state, 4);
  assert.equal(state.visible, "abcd");
  state = setTypewriterTarget(state, "abXY");
  assert.equal(state.visible, "ab");
});

test("flushTypewriter and empty target reset", () => {
  let state = setTypewriterTarget(createTypewriterState(), "短句");
  state = tickTypewriter(state, 1);
  state = flushTypewriter(state);
  assert.equal(state.visible, "短句");
  state = setTypewriterTarget(state, "");
  assert.deepEqual(state, createTypewriterState("", ""));
});

test("typewriterPartialActive and holdBackMatchingAssistant", () => {
  let state = setTypewriterTarget(createTypewriterState(), "完整回复");
  assert.equal(typewriterPartialActive(state, true), true);
  state = tickTypewriter(state, 2);
  assert.equal(typewriterPartialActive(state, false), true);
  state = flushTypewriter(state);
  assert.equal(typewriterPartialActive(state, false), false);

  const transcript = [
    { role: "user", text: "hi" },
    { role: "assistant", text: "完整回复" },
  ];
  assert.deepEqual(
    holdBackMatchingAssistant(transcript, "完整回复", false, false),
    [{ role: "user", text: "hi" }],
  );
  assert.deepEqual(
    holdBackMatchingAssistant(transcript, "完整回复", false, true),
    transcript,
  );
  assert.deepEqual(
    holdBackMatchingAssistant(transcript, "完整回复", true, false),
    transcript,
  );
});
