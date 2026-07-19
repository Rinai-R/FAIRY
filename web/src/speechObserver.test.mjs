import test from "node:test";
import assert from "node:assert/strict";

import {
  createSpeechObserver,
  reduceSpeechObserver,
  revealSpeechObserverThrough,
  speechBubbleVisible,
} from "./speechObserver.mjs";

const TURN_A = "11111111-1111-4111-8111-111111111111";
const TURN_B = "22222222-2222-4222-8222-222222222222";

function stateChanged(turnId, state) {
  return { turnId, state, payload: { type: "state_changed" } };
}

function replyChain(turnId, index, text, visualState = "idle") {
  return {
    turnId,
    state: "responding",
    payload: {
      type: "reply_chain",
      index,
      delta: index === 0 ? text : `\n${text}`,
      text,
      speechText: text,
      visualState,
    },
  };
}

function completed(turnId, text, chains) {
  return {
    turnId,
    state: "completed",
    payload: {
      type: "completed",
      text,
      chains: chains ?? [{ text, visualState: "idle" }],
    },
  };
}

function speechRequested(turnId, text) {
  return { turnId, state: "completed", payload: { type: "speech.requested", text } };
}

function speechSynthesized(turnId, dataUrl) {
  return { turnId, state: "completed", payload: { type: "speech.synthesized", index: 0, text: "嗨，在的", dataUrl } };
}

test("a new turn starts in the waiting state before any text arrives", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, stateChanged(TURN_A, "interpreting"));
  assert.equal(observer.turnId, TURN_A);
  assert.equal(observer.waiting, true);
  assert.equal(observer.draft, "");
  assert.equal(speechBubbleVisible(observer), true);
});

test("each reply chain is its own bubble, revealed one at a time", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, stateChanged(TURN_A, "interpreting"));
  observer = reduceSpeechObserver(observer, replyChain(TURN_A, 0, "你好"));
  // reply_chain alone must not reveal — 齐套 beat.ready does.
  assert.equal(observer.draft, "");
  assert.equal(observer.chains.length, 1);
  observer = reduceSpeechObserver(observer, {
    turnId: TURN_A,
    state: "responding",
    payload: {
      type: "beat.ready",
      beatId: "final-0",
      kind: "final",
      index: 0,
      chainIndex: 0,
      displayText: "你好",
      speechText: "你好",
      visualState: "idle",
      dataUrl: "",
    },
  });
  assert.equal(observer.draft, "你好");
  observer = reduceSpeechObserver(observer, replyChain(TURN_A, 1, "呀"));
  assert.equal(observer.draft, "你好");
  assert.equal(observer.chains.length, 2);
  assert.equal(observer.waiting, false);
  assert.equal(speechBubbleVisible(observer), true);
  observer = reduceSpeechObserver(observer, {
    turnId: TURN_A,
    state: "responding",
    payload: {
      type: "beat.ready",
      beatId: "final-1",
      kind: "final",
      index: 1,
      chainIndex: 1,
      displayText: "呀",
      speechText: "呀",
      visualState: "idle",
      dataUrl: "",
    },
  });
  assert.equal(observer.draft, "呀");
});

test("completion shows only the currently revealed beat", () => {
  let observer = createSpeechObserver();
  observer = reduceSpeechObserver(observer, replyChain(TURN_A, 0, "嗨"));
  observer = reduceSpeechObserver(observer, replyChain(TURN_A, 1, "在的"));
  observer = reduceSpeechObserver(
    observer,
    completed(TURN_A, "嗨\n在的", [
      { text: "嗨", visualState: "idle" },
      { text: "在的", visualState: "happy" },
    ]),
  );
  // The reveal position is unchanged, so the bubble still shows the first beat.
  assert.equal(observer.draft, "嗨");
  assert.equal(observer.chains.length, 2);
  assert.equal(observer.active, true);
  observer = revealSpeechObserverThrough(observer, 1);
  assert.equal(observer.draft, "在的");
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
  assert.equal(observer.revealThrough, -1);
  assert.equal(observer.chains.length, 0);
  // Contract for CharacterSpeechBubble: waiting chrome only — never keep the
  // previous turn's text under the thinking dots (send-while-speaking).
  assert.equal(speechBubbleVisible(observer), true);
});

test("interrupt and failure clear the bubble", () => {
  let waiting = reduceSpeechObserver(createSpeechObserver(), replyChain(TURN_A, 0, "写到一半"));
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
