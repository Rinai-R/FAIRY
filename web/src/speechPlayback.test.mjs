import test from "node:test";
import assert from "node:assert/strict";

import {
  advanceSpeechPlayback,
  createSpeechPlaybackState,
  currentSpeechSegment,
  playSpeechDataUrl,
  reduceSpeechPlayback,
  speechBubbleMayFade,
} from "./speechPlayback.mjs";

const TURN_ID = "11111111-1111-4111-8111-111111111111";

test("speech playback holds bubble from speech.requested until audio finishes", () => {
  let state = reduceSpeechPlayback(createSpeechPlaybackState(), {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "speech.requested",
      text: "こんにちは。",
      characterRevision: 1,
      userProfileRevision: null,
    },
  });
  assert.equal(state.hold, true);
  assert.equal(state.synthesisComplete, false);
  assert.equal(speechBubbleMayFade(state), false);

  state = reduceSpeechPlayback(state, {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "speech.synthesized",
      index: 0,
      text: "こんにちは。",
      dataUrl: "data:audio/mpeg;base64,ZmFrZQ==",
    },
  });
  assert.equal(state.hold, true);
  assert.equal(state.dataUrl, "data:audio/mpeg;base64,ZmFrZQ==");
  assert.equal(speechBubbleMayFade(state), false);

  // First beat ends before completed: keep holding while more TTS may arrive.
  state = advanceSpeechPlayback(state);
  assert.equal(state.played, false);
  assert.equal(state.hold, true);
  assert.equal(state.dataUrl, "");

  state = reduceSpeechPlayback(state, {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "completed",
      text: "こんにちは。",
      speechText: "こんにちは。",
      sources: [],
      characterRevision: 1,
      userProfileRevision: null,
      usage: [],
      visualState: "idle",
      chains: [{ text: "こんにちは。", speechText: "こんにちは。", visualState: "idle" }],
    },
  });
  assert.equal(state.synthesisComplete, true);
  assert.equal(state.played, true);
  assert.equal(state.hold, false);
  assert.equal(speechBubbleMayFade(state), true);
});

test("speech playback waits for late segments instead of fading early", () => {
  let state = reduceSpeechPlayback(createSpeechPlaybackState(), {
    turnId: TURN_ID,
    state: "responding",
    payload: {
      type: "speech.synthesized",
      index: 0,
      text: "一句",
      dataUrl: "data:audio/mpeg;base64,one",
    },
  });
  // Beat 0 finishes before beat 1 is synthesized.
  state = advanceSpeechPlayback(state);
  assert.equal(state.hold, true);
  assert.equal(state.played, false);
  assert.equal(state.dataUrl, "");
  assert.equal(speechBubbleMayFade(state), false);

  state = reduceSpeechPlayback(state, {
    turnId: TURN_ID,
    state: "responding",
    payload: {
      type: "speech.synthesized",
      index: 1,
      text: "二句",
      dataUrl: "data:audio/mpeg;base64,two",
    },
  });
  assert.equal(state.hold, true);
  assert.equal(state.played, false);
  assert.equal(state.dataUrl, "data:audio/mpeg;base64,two");
  assert.equal(currentSpeechSegment(state)?.index, 1);

  state = advanceSpeechPlayback(state);
  assert.equal(state.hold, true);
  assert.equal(state.played, false);

  state = reduceSpeechPlayback(state, {
    turnId: TURN_ID,
    state: "completed",
    payload: { type: "completed" },
  });
  assert.equal(state.synthesisComplete, true);
  assert.equal(state.played, true);
  assert.equal(state.hold, false);
});

test("speech playback queues multiple chain segments in index order", () => {
  let state = reduceSpeechPlayback(createSpeechPlaybackState(), {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "speech.synthesized",
      index: 0,
      text: "一句",
      dataUrl: "data:audio/mpeg;base64,one",
    },
  });
  state = reduceSpeechPlayback(state, {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "speech.synthesized",
      index: 1,
      text: "二句",
      dataUrl: "data:audio/mpeg;base64,two",
    },
  });
  assert.deepEqual(
    state.segments.map((item) => item.index),
    [0, 1],
  );
  assert.equal(state.dataUrl, "data:audio/mpeg;base64,one");
  state = advanceSpeechPlayback(state);
  assert.equal(state.playIndex, 1);
  assert.equal(state.dataUrl, "data:audio/mpeg;base64,two");
  assert.equal(state.played, false);
  assert.equal(state.hold, true);
  state = advanceSpeechPlayback(state);
  assert.equal(state.played, false);
  assert.equal(state.hold, true);
  state = reduceSpeechPlayback(state, {
    turnId: TURN_ID,
    state: "completed",
    payload: { type: "completed" },
  });
  assert.equal(state.played, true);
  assert.equal(state.hold, false);
});

test("speech playback sorts late-arriving earlier segments to the front", () => {
  let state = reduceSpeechPlayback(createSpeechPlaybackState(), {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "speech.synthesized",
      index: 1,
      text: "二句",
      dataUrl: "data:audio/mpeg;base64,two",
    },
  });
  assert.equal(state.dataUrl, "data:audio/mpeg;base64,two");
  state = reduceSpeechPlayback(state, {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "speech.synthesized",
      index: 0,
      text: "一句",
      dataUrl: "data:audio/mpeg;base64,one",
    },
  });
  assert.deepEqual(
    state.segments.map((item) => item.index),
    [0, 1],
  );
  assert.equal(state.playIndex, 0);
  assert.equal(state.dataUrl, "data:audio/mpeg;base64,one");
});

test("segments carry chainIndex; utterance audio (chainIndex -1) plays before chains", () => {
  // Utterance audio arrives first at playback index 0 with chainIndex -1.
  let state = reduceSpeechPlayback(createSpeechPlaybackState(), {
    turnId: TURN_ID,
    state: "planning",
    payload: {
      type: "speech.synthesized",
      index: 0,
      chainIndex: -1,
      text: "让我看看",
      dataUrl: "data:audio/mpeg;base64,utter",
    },
  });
  assert.equal(currentSpeechSegment(state)?.chainIndex, -1);
  assert.equal(state.dataUrl, "data:audio/mpeg;base64,utter");

  // Then chain 0 audio at playback index 1.
  state = reduceSpeechPlayback(state, {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "speech.synthesized",
      index: 1,
      chainIndex: 0,
      text: "找到了",
      dataUrl: "data:audio/mpeg;base64,chain0",
    },
  });
  assert.deepEqual(
    state.segments.map((item) => [item.index, item.chainIndex]),
    [[0, -1], [1, 0]],
  );
  // Utterance still plays first (playback order by index).
  assert.equal(state.playIndex, 0);
  state = advanceSpeechPlayback(state);
  assert.equal(currentSpeechSegment(state)?.chainIndex, 0);
});

test("speech playback keeps bubble held after TTS failure", () => {
  const error = { code: "TTS_FAILED", message: "failed", retryable: false };
  const state = reduceSpeechPlayback(createSpeechPlaybackState(), {
    turnId: TURN_ID,
    state: "completed",
    payload: { type: "speech.failed", error },
  });
  assert.deepEqual(state.error, error);
  assert.equal(state.dataUrl, "");
  assert.equal(state.segments.length, 0);
  assert.equal(state.hold, true);
  assert.equal(state.synthesisComplete, true);
  assert.equal(speechBubbleMayFade(state), false);
});

test("playSpeechDataUrl waits for ended before resolving", async () => {
  const played = [];
  class FakeAudio {
    constructor(src) {
      this.src = src;
      this.volume = 1;
      this.listeners = new Map();
      played.push(src);
    }
    addEventListener(type, fn) {
      this.listeners.set(type, fn);
    }
    play() {
      queueMicrotask(() => this.listeners.get("ended")?.());
      return Promise.resolve();
    }
  }
  await playSpeechDataUrl("data:audio/mpeg;base64,ZmFrZQ==", FakeAudio);
  assert.deepEqual(played, ["data:audio/mpeg;base64,ZmFrZQ=="]);
});

test("playSpeechDataUrl can mute the timing clone", async () => {
  let volume = null;
  class FakeAudio {
    constructor() {
      this.volume = 1;
      this.listeners = new Map();
    }
    addEventListener(type, fn) {
      this.listeners.set(type, fn);
    }
    play() {
      volume = this.volume;
      queueMicrotask(() => this.listeners.get("ended")?.());
      return Promise.resolve();
    }
  }
  await playSpeechDataUrl("data:audio/mpeg;base64,ZmFrZQ==", FakeAudio, { muted: true });
  assert.equal(volume, 0);
});

test("playSpeechDataUrl rejects non-audio input", async () => {
  await assert.rejects(() => playSpeechDataUrl("https://example.test/a.mp3", class {}), /audio URL/);
});
