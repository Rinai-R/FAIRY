import test from "node:test";
import assert from "node:assert/strict";

import {
  createSpeechObserver,
  reduceSpeechObserver,
  speechBubbleVisible,
} from "./speechObserver.mjs";

const TURN_A = "11111111-1111-4111-8111-111111111111";
const TURN_B = "22222222-2222-4222-8222-222222222222";

function stateChanged(turnId, state) {
  return { turnId, state, payload: { type: "state_changed" } };
}

function replyChain(turnId, delta) {
  return { turnId, state: "responding", payload: { type: "reply_chain", delta } };
}

function completed(turnId, text) {
  return { turnId, state: "completed", payload: { type: "completed", text } };
}

function speechRequested(turnId, text) {
  return { turnId, state: "completed", payload: { type: "speech.requested", text } };
}

function speechSynthesized(turnId, dataUrl) {
  return { turnId, state: "completed", payload: { type: "speech.synthesized", text: "嗨，在的", dataUrl } };
}

test("a new turn starts in the waiting state before any text arrives", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, stateChanged(TURN_A, "interpreting"));
  assert.equal(observer.turnId, TURN_A);
  assert.equal(observer.waiting, true);
  assert.equal(observer.draft, "");
  assert.equal(speechBubbleVisible(observer), true);
});

test("streamed reply chains accumulate the draft and clear waiting", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, stateChanged(TURN_A, "interpreting"));
  observer = reduceSpeechObserver(observer, replyChain(TURN_A, "你好"));
  observer = reduceSpeechObserver(observer, replyChain(TURN_A, "呀"));
  assert.equal(observer.draft, "你好呀");
  assert.equal(observer.waiting, false);
  assert.equal(speechBubbleVisible(observer), true);
});

test("completion pins the full text", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, replyChain(TURN_A, "嗨"));
  observer = reduceSpeechObserver(observer, completed(TURN_A, "嗨，在的"));
  assert.equal(observer.draft, "嗨，在的");
  assert.equal(observer.active, true);
});

test("speech requested records the text without changing the visible draft", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, completed(TURN_A, "嗨，在的"));
  observer = reduceSpeechObserver(observer, speechRequested(TURN_A, "嗨，在的"));
  assert.equal(observer.draft, "嗨，在的");
  assert.deepEqual(observer.speechRequest, { turnId: TURN_A, text: "嗨，在的" });
  assert.equal(observer.waiting, false);
});

test("speech synthesized does not change visible draft", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, completed(TURN_A, "嗨，在的"));
  const next = reduceSpeechObserver(observer, speechSynthesized(TURN_A, "data:audio/mpeg;base64,ZmFrZQ=="));
  assert.equal(next.draft, "嗨，在的");
  assert.equal(next.speechRequest, null);
});

test("a new turn resets the previous draft", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, completed(TURN_A, "第一轮"));
  observer = reduceSpeechObserver(observer, stateChanged(TURN_B, "interpreting"));
  assert.equal(observer.turnId, TURN_B);
  assert.equal(observer.draft, "");
  assert.equal(observer.waiting, true);
});

test("interrupt and failure clear the bubble", () => {
  let waiting = reduceSpeechObserver(createSpeechObserver(), replyChain(TURN_A, "写到一半"));
  const interrupted = reduceSpeechObserver(waiting, stateChanged(TURN_A, "interrupted"));
  assert.equal(interrupted.active, false);
  assert.equal(speechBubbleVisible(interrupted), false);

  const failed = reduceSpeechObserver(waiting, {
    turnId: TURN_A,
    state: "failed",
    payload: { type: "failed", error: { code: "X", message: "y", retryable: true } },
  });
  assert.equal(failed.active, false);
  assert.equal(speechBubbleVisible(failed), false);
});
