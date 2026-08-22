/**
 * Daily speech bubble: assistant-only target text with auto-fade after dwell.
 */

export const SPEECH_BUBBLE_FADE_AFTER_MS = 4000;

export function createSpeechBubbleState() {
  return Object.freeze({
    target: "",
    fading: false,
    fadeStartedAt: null,
  });
}

export function reduceSpeechBubbleState(state, action) {
  if (state === null || typeof state !== "object" || Object.isFrozen(state) === false) {
    throw new TypeError("speech bubble state must be immutable");
  }
  if (action === null || typeof action !== "object" || Array.isArray(action)) {
    throw new TypeError("speech bubble action must be an object");
  }
  switch (action.type) {
    case "set_target": {
      const target = typeof action.target === "string" ? action.target : "";
      if (target === "") {
        return createSpeechBubbleState();
      }
      return Object.freeze({
        target,
        fading: false,
        fadeStartedAt: null,
      });
    }
    case "start_fade": {
      if (state.target.length === 0 || state.fading) return state;
      const at = typeof action.at === "number" ? action.at : Date.now();
      return Object.freeze({
        ...state,
        fading: true,
        fadeStartedAt: at,
      });
    }
    case "clear":
      return createSpeechBubbleState();
    default:
      throw new TypeError(`unsupported speech bubble action: ${String(action.type)}`);
  }
}

export function shouldClearFadedSpeechBubble(state, now = Date.now()) {
  if (!state.fading || state.fadeStartedAt === null) return false;
  return now - state.fadeStartedAt >= SPEECH_BUBBLE_FADE_AFTER_MS;
}
