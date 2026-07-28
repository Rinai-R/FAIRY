const PLAYBACK_ERROR = "语音播放失败";
const PLAYBACK_ORDER_ERROR = "语音顺序无效";

export function createSpeechPlayback({ createAudio, onStateChange = () => {} } = {}) {
  if (typeof createAudio !== "function") {
    throw new TypeError("createAudio is required");
  }
  if (typeof onStateChange !== "function") {
    throw new TypeError("onStateChange must be a function");
  }

  let turnId = "";
  let lastIndex = -1;
  let queue = [];
  let active = null;
  let error = "";

  const snapshot = () => Object.freeze({
    turnId,
    lastIndex,
    active: active !== null,
    queued: queue.length,
    busy: active !== null || queue.length > 0,
    error,
  });
  const notify = () => onStateChange(snapshot());

  const release = (entry) => {
    if (!entry?.audio) return;
    entry.audio.onended = null;
    entry.audio.onerror = null;
    try { entry.audio.pause?.(); } catch {}
    try {
      if (typeof entry.audio.removeAttribute === "function") entry.audio.removeAttribute("src");
      else entry.audio.src = "";
    } catch {}
    try { entry.audio.load?.(); } catch {}
  };

  const advance = () => {
    if (active !== null || queue.length === 0) {
      notify();
      return;
    }
    const item = queue.shift();
    let audio;
    try {
      audio = createAudio(item.dataUrl);
    } catch {
      error = PLAYBACK_ERROR;
      notify();
      advance();
      return;
    }
    const entry = { item, audio };
    active = entry;
    const finish = (message = "") => {
      if (active !== entry) return;
      release(entry);
      active = null;
      if (message) error = message;
      notify();
      advance();
    };
    audio.onended = () => finish();
    audio.onerror = () => finish(PLAYBACK_ERROR);
    notify();
    try {
      Promise.resolve(audio.play()).catch(() => finish(PLAYBACK_ERROR));
    } catch {
      finish(PLAYBACK_ERROR);
    }
  };

  const reset = (nextTurnId = "") => {
    const previous = active;
    active = null;
    queue = [];
    release(previous);
    turnId = nextTurnId;
    lastIndex = -1;
    error = "";
    notify();
  };

  return Object.freeze({
    enqueue(nextTurnId, beat) {
      const nextID = typeof nextTurnId === "string" ? nextTurnId.trim() : "";
      const dataUrl = typeof beat?.dataUrl === "string" ? beat.dataUrl : "";
      if (!nextID || !dataUrl) return false;
      if (turnId && turnId !== nextID) reset(nextID);
      else if (!turnId) turnId = nextID;
      const index = beat?.index;
      if (!Number.isSafeInteger(index) || index < 0 || index <= lastIndex) {
        error = PLAYBACK_ORDER_ERROR;
        notify();
        return false;
      }
      lastIndex = index;
      queue.push({ turnId: nextID, index, beatId: beat?.beatId || "", dataUrl });
      advance();
      return true;
    },
    beginTurn(nextTurnId = "") {
      reset(typeof nextTurnId === "string" ? nextTurnId.trim() : "");
    },
    stop() {
      reset("");
    },
    snapshot,
  });
}
