import test from "node:test";
import assert from "node:assert/strict";

import {
  createSpeechPlaybackState,
  playSpeechDataUrl,
  reduceSpeechPlayback,
} from "./speechPlayback.mjs";

const TURN_ID = "11111111-1111-4111-8111-111111111111";

test("speech playback records synthesized audio data URL", () => {
  const state = reduceSpeechPlayback(createSpeechPlaybackState(), {
    turnId: TURN_ID,
    state: "completed",
    payload: {
      type: "speech.synthesized",
      text: "こんにちは。",
      dataUrl: "data:audio/mpeg;base64,ZmFrZQ==",
    },
  });
  assert.equal(state.turnId, TURN_ID);
  assert.equal(state.dataUrl, "data:audio/mpeg;base64,ZmFrZQ==");
  assert.equal(state.played, false);
});

test("speech playback records failed speech event", () => {
  const error = { code: "TTS_FAILED", message: "failed", retryable: false };
  const state = reduceSpeechPlayback(createSpeechPlaybackState(), {
    turnId: TURN_ID,
    state: "completed",
    payload: { type: "speech.failed", error },
  });
  assert.deepEqual(state.error, error);
  assert.equal(state.dataUrl, "");
});

test("playSpeechDataUrl uses injected Audio constructor", async () => {
  const played = [];
  class FakeAudio {
    constructor(src) {
      this.src = src;
      played.push(src);
    }
    play() {
      return Promise.resolve();
    }
  }
  await playSpeechDataUrl("data:audio/mpeg;base64,ZmFrZQ==", FakeAudio);
  assert.deepEqual(played, ["data:audio/mpeg;base64,ZmFrZQ=="]);
});

test("playSpeechDataUrl rejects non-audio input", async () => {
  await assert.rejects(() => playSpeechDataUrl("https://example.test/a.mp3", class {}), /audio URL/);
});
