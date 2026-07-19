/**
 * Daily speech bubble: assistant-only target text with auto-fade after dwell.
 */

export const SPEECH_BUBBLE_FADE_AFTER_MS = 4000;
export const SPEECH_BUBBLE_POST_AUDIO_FADE_MS = 800;

export function createSpeechBubbleState() {
  return Object.freeze({
    target: "",
    fading: false,
    fadeStartedAt: null,
  });
}

/**
 * Waiting dots and reply text must never share the bubble. Used when a new turn
 * starts while the previous reply is still on screen (send-while-speaking).
 */
export function speechBubbleSurface(targetText, waiting) {
  const hasTarget = typeof targetText === "string" && targetText.length > 0;
  if (waiting && !hasTarget) {
    return Object.freeze({ mode: "waiting", showText: false, showWaiting: true });
  }
  if (hasTarget) {
    return Object.freeze({ mode: "text", showText: true, showWaiting: false });
  }
  return Object.freeze({ mode: "hidden", showText: false, showWaiting: false });
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
