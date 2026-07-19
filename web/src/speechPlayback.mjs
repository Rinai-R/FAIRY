export function createSpeechPlaybackState() {
  return Object.freeze({
    turnId: null,
    segments: Object.freeze([]),
    playIndex: 0,
    dataUrl: "",
    error: null,
    played: false,
    /** When true, bubble must stay until audio finishes (or forever on TTS failure). */
    hold: false,
    /**
     * False until the turn's `completed` (or speech.failed) arrives. While false,
     * advancing past the last known segment waits for more TTS instead of fading.
     */
    synthesisComplete: false,
  });
}

function playbackWithSegments(turnId, segments, playIndex, error, hold, synthesisComplete) {
  const dataUrl = playIndex < segments.length ? segments[playIndex].dataUrl : "";
  return Object.freeze({
    turnId,
    segments: Object.freeze(segments),
    playIndex,
    dataUrl,
    error,
    played: false,
    hold,
    synthesisComplete,
  });
}

function upsertSpeechSegment(segments, segment) {
  const byIndex = new Map(segments.map((item) => [item.index, item]));
  byIndex.set(segment.index, Object.freeze(segment));
  return [...byIndex.values()].sort((left, right) => left.index - right.index);
}

function playIndexForChainIndex(segments, chainIndex) {
  const exact = segments.findIndex((item) => item.index === chainIndex);
  if (exact >= 0) return exact;
  const next = segments.findIndex((item) => item.index > chainIndex);
  return next >= 0 ? next : Math.max(segments.length - 1, 0);
}

export function currentSpeechSegment(state) {
  if (!state || !Array.isArray(state.segments)) return null;
  if (state.playIndex < 0 || state.playIndex >= state.segments.length) return null;
  return state.segments[state.playIndex] ?? null;
}

export function reduceSpeechPlayback(state, event) {
  const payload = event?.payload;
  if (!payload) return state;
  if (
    event.state !== "completed"
    && event.state !== "responding"
    && event.state !== "planning"
  ) {
    return state;
  }
  switch (payload.type) {
    case "speech.requested":
      return Object.freeze({
        turnId: event.turnId,
        segments: Object.freeze([]),
        playIndex: 0,
        dataUrl: "",
        error: null,
        played: false,
        hold: true,
        synthesisComplete: false,
      });
    case "speech.synthesized": {
      const segment = Object.freeze({
        // index is playback order; chainIndex maps audio back to its reply chain
        // (-1 for mid-ReAct utterance audio, which must not reveal a chain bubble).
        index: payload.index,
        chainIndex: Number.isSafeInteger(payload.chainIndex) ? payload.chainIndex : payload.index,
        dataUrl: payload.dataUrl,
      });
      if (event.turnId !== state.turnId) {
        return playbackWithSegments(event.turnId, [segment], 0, null, true, false);
      }
      const segments = upsertSpeechSegment([...state.segments], segment);
      let playIndex = state.playIndex;
      // Premature "finished" before later beats arrived: reopen at this segment.
      if (state.played && !state.synthesisComplete) {
        playIndex = playIndexForChainIndex(segments, segment.index);
      } else if (playIndex >= segments.length) {
        playIndex = playIndexForChainIndex(segments, segment.index);
      }
      // Otherwise keep playIndex — a still-playing beat continues; a waiting
      // playIndex that already pointed past the old end now lands on the new beat.
      return playbackWithSegments(
        state.turnId,
        segments,
        playIndex,
        null,
        true,
        state.synthesisComplete,
      );
    }
    case "beat.ready": {
      // Paired text+audio unit. Audio (when present) enters the playback queue;
      // text-only beats do not hold the bubble waiting for sound that will never come.
      const dataUrl = typeof payload.dataUrl === "string" ? payload.dataUrl : "";
      if (!dataUrl) {
        if (event.turnId !== state.turnId) {
          return Object.freeze({
            ...createSpeechPlaybackState(),
            turnId: event.turnId,
            synthesisComplete: false,
          });
        }
        return state;
      }
      const segment = Object.freeze({
        index: payload.index,
        chainIndex: Number.isSafeInteger(payload.chainIndex) ? payload.chainIndex : -1,
        dataUrl,
        beatId: typeof payload.beatId === "string" ? payload.beatId : "",
      });
      if (event.turnId !== state.turnId) {
        return playbackWithSegments(event.turnId, [segment], 0, null, true, false);
      }
      const segments = upsertSpeechSegment([...state.segments], segment);
      let playIndex = state.playIndex;
      if (state.played && !state.synthesisComplete) {
        playIndex = playIndexForChainIndex(segments, segment.index);
      } else if (playIndex >= segments.length) {
        playIndex = playIndexForChainIndex(segments, segment.index);
      }
      return playbackWithSegments(
        state.turnId,
        segments,
        playIndex,
        null,
        true,
        state.synthesisComplete,
      );
    }
    case "speech.failed":
      return Object.freeze({
        turnId: event.turnId,
        segments: Object.freeze([]),
        playIndex: 0,
        dataUrl: "",
        error: payload.error,
        played: false,
        hold: true,
        synthesisComplete: true,
      });
    case "completed": {
      if (state.turnId !== null && event.turnId !== state.turnId) {
        return state;
      }
      const turnId = state.turnId ?? event.turnId;
      // Still have queued / current audio — keep holding through playback.
      if (state.playIndex < state.segments.length) {
        return Object.freeze({
          ...state,
          turnId,
          synthesisComplete: true,
          hold: true,
          played: false,
        });
      }
      // Waiting for more that will never arrive, or no speech at all.
      return Object.freeze({
        ...state,
        turnId,
        synthesisComplete: true,
        played: true,
        hold: false,
        dataUrl: "",
      });
    }
    default:
      return state;
  }
}

export function advanceSpeechPlayback(state) {
  const next = state.playIndex + 1;
  if (next < state.segments.length) {
    return playbackWithSegments(
      state.turnId,
      [...state.segments],
      next,
      null,
      true,
      state.synthesisComplete,
    );
  }
  // End of known segments. If more TTS may still arrive, wait instead of fading.
  if (!state.synthesisComplete) {
    return Object.freeze({
      ...state,
      playIndex: next,
      dataUrl: "",
      played: false,
      hold: true,
    });
  }
  return Object.freeze({
    ...state,
    playIndex: next,
    played: true,
    hold: false,
    dataUrl: "",
  });
}

/** True when the bubble may start its post-reply fade. */
export function speechBubbleMayFade(playback) {
  if (!playback || typeof playback !== "object") return true;
  return playback.hold !== true;
}

export function playSpeechDataUrl(dataUrl, AudioCtor = globalThis.Audio, options = {}) {
  if (typeof dataUrl !== "string" || !dataUrl.startsWith("data:audio/")) {
    return Promise.reject(new TypeError("speech audio must be a data audio URL"));
  }
  if (typeof AudioCtor !== "function") {
    return Promise.reject(new Error("audio playback is unavailable"));
  }
  const audio = new AudioCtor(dataUrl);
  if (options.muted === true && typeof audio === "object" && audio !== null) {
    audio.volume = 0;
  }
  let settled = false;
  const stop = () => {
    try {
      if (typeof audio.pause === "function") audio.pause();
    } catch {
      // ignore teardown errors from test doubles / already-released audio
    }
    try {
      audio.src = "";
    } catch {
      // ignore
    }
  };
  const promise = new Promise((resolve, reject) => {
    const finish = (fn, value) => {
      if (settled) return;
      settled = true;
      fn(value);
    };
    const onEnded = () => finish(resolve);
    const onError = () => finish(reject, new Error("speech audio playback failed"));
    if (typeof audio.addEventListener === "function") {
      audio.addEventListener("ended", onEnded, { once: true });
      audio.addEventListener("error", onError, { once: true });
    }
    const played = audio.play();
    if (played && typeof played.then === "function") {
      played.catch((error) => finish(reject, error));
    }
    // Test doubles may omit ended events; resolve after play() if no listener API.
    if (typeof audio.addEventListener !== "function") {
      finish(resolve);
    }
  });
  promise.stop = stop;
  return promise;
}
