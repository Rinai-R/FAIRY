// The standalone speech-bubble window has no companion session of its own. It
// passively rebuilds just enough turn state (streamed draft + waiting flag) from
// the globally broadcast harness events to render the bubble. Unlike
// reduceCompanionState this is a lenient observer: it never throws on ordering
// and simply tracks the most recent turn.

export function createSpeechObserver() {
  return Object.freeze({
    turnId: null,
    draft: "",
    waiting: false,
    active: false,
  });
}

// reduceSpeechObserver consumes an already-parsed harness event (the shape
// returned by parseHarnessEvent).
export function reduceSpeechObserver(state, event) {
  const base = event.turnId === state.turnId
    ? state
    : { turnId: event.turnId, draft: "", waiting: true, active: true };

  const payload = event.payload;
  switch (payload.type) {
    case "text_delta":
    case "reply_chain":
      return Object.freeze({
        ...base,
        draft: base.draft + payload.delta,
        waiting: false,
        active: true,
      });
    case "completed":
      return Object.freeze({
        ...base,
        draft: payload.text,
        waiting: false,
        active: true,
      });
    case "failed":
      return Object.freeze({ ...base, draft: "", waiting: false, active: false });
    case "state_changed":
      if (event.state === "interrupted") {
        return Object.freeze({ ...base, draft: "", waiting: false, active: false });
      }
      return Object.freeze({
        ...base,
        waiting: base.draft.length === 0,
        active: true,
      });
    default:
      // speech.requested and any other trailing events do not change the bubble.
      return base === state ? state : Object.freeze(base);
  }
}

// speechBubbleVisible reports whether the bubble currently has something to show
// (streaming text or a waiting indicator for the active turn).
export function speechBubbleVisible(state) {
  return state.active && (state.draft.length > 0 || state.waiting);
}
