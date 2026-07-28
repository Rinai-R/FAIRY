import assert from "node:assert/strict";
import test from "node:test";

import { createSpeechPlayback } from "./speechPlayback.mjs";

class FakeAudio {
  constructor(src, playResult = Promise.resolve()) {
    this.src = src;
    this.playResult = playResult;
    this.playCalls = 0;
    this.pauseCalls = 0;
    this.loadCalls = 0;
    this.removedSource = false;
    this.onended = null;
    this.onerror = null;
  }

  play() {
    this.playCalls += 1;
    return this.playResult;
  }

  pause() {
    this.pauseCalls += 1;
  }

  removeAttribute(name) {
    if (name === "src") {
      this.removedSource = true;
      this.src = "";
    }
  }

  load() {
    this.loadCalls += 1;
  }

  end() {
    this.onended?.();
  }

  fail() {
    this.onerror?.();
  }
}

function audioBeat(index) {
  return {
    beatId: `beat-${index}`,
    index,
    dataUrl: `data:audio/mpeg;base64,${index}`,
  };
}

test("speech playback serializes beats from the same turn", () => {
  const created = [];
  const playback = createSpeechPlayback({
    createAudio: (src) => {
      const audio = new FakeAudio(src);
      created.push(audio);
      return audio;
    },
  });

  assert.equal(playback.enqueue("turn-1", audioBeat(0)), true);
  assert.equal(playback.enqueue("turn-1", audioBeat(1)), true);
  assert.equal(created.length, 1);
  assert.equal(created[0].playCalls, 1);
  assert.deepEqual(playback.snapshot(), {
    turnId: "turn-1", lastIndex: 1, active: true, queued: 1, busy: true, error: "",
  });

  created[0].end();
  assert.equal(created.length, 2);
  assert.equal(created[1].playCalls, 1);
  assert.equal(created[0].pauseCalls, 1);
  assert.equal(created[0].src, "");

  created[1].end();
  assert.equal(playback.snapshot().busy, false);
  assert.equal(playback.snapshot().queued, 0);
});

test("speech playback ignores text-only beats without creating Audio", () => {
  let creations = 0;
  const playback = createSpeechPlayback({
    createAudio: () => {
      creations += 1;
      return new FakeAudio("");
    },
  });

  assert.equal(playback.enqueue("turn-1", { beatId: "text", index: 0 }), false);
  assert.equal(creations, 0);
  assert.equal(playback.snapshot().busy, false);
});

test("speech playback replaces an obsolete turn and releases its media", () => {
  const created = [];
  const playback = createSpeechPlayback({
    createAudio: (src) => {
      const audio = new FakeAudio(src);
      created.push(audio);
      return audio;
    },
  });

  playback.enqueue("turn-old", audioBeat(0));
  playback.enqueue("turn-old", audioBeat(1));
  playback.enqueue("turn-new", audioBeat(0));
  assert.equal(created.length, 2);
  assert.equal(created[0].pauseCalls, 1);
  assert.equal(created[0].removedSource, true);
  assert.equal(created[0].src, "");
  assert.equal(playback.snapshot().turnId, "turn-new");
  assert.equal(playback.snapshot().queued, 0);

  playback.enqueue("turn-new", audioBeat(1));
  assert.equal(playback.snapshot().queued, 1);
  playback.stop();
  assert.equal(created[1].pauseCalls, 1);
  assert.equal(created[1].src, "");
  assert.deepEqual(playback.snapshot(), {
    turnId: "", lastIndex: -1, active: false, queued: 0, busy: false, error: "",
  });
});

test("speech playback reports play rejection and continues the queue", async () => {
  const created = [];
  const states = [];
  const playback = createSpeechPlayback({
    createAudio: (src) => {
      const audio = new FakeAudio(
        src,
        created.length === 0
          ? Promise.reject(new Error("Authorization: secret"))
          : Promise.resolve(),
      );
      created.push(audio);
      return audio;
    },
    onStateChange: (state) => states.push(state),
  });

  playback.enqueue("turn-1", audioBeat(0));
  playback.enqueue("turn-1", audioBeat(1));
  await Promise.resolve();
  await Promise.resolve();

  assert.equal(created.length, 2);
  assert.equal(created[0].src, "");
  assert.equal(created[1].playCalls, 1);
  assert.equal(playback.snapshot().error, "语音播放失败");
  assert.equal(states.some((state) => state.error.includes("secret")), false);
  created[1].end();
  assert.equal(playback.snapshot().busy, false);
});

test("speech playback continues after a media error and rejects duplicate order", () => {
  const created = [];
  const playback = createSpeechPlayback({
    createAudio: (src) => {
      const audio = new FakeAudio(src);
      created.push(audio);
      return audio;
    },
  });

  assert.equal(playback.enqueue("turn-1", audioBeat(3)), true);
  assert.equal(playback.enqueue("turn-1", audioBeat(3)), false);
  assert.equal(playback.snapshot().error, "语音顺序无效");
  assert.equal(playback.enqueue("turn-1", audioBeat(4)), true);
  created[0].fail();
  assert.equal(created.length, 2);
  assert.equal(created[1].playCalls, 1);
  assert.equal(playback.snapshot().error, "语音播放失败");
});
