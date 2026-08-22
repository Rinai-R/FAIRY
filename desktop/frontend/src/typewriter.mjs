/**
 * Display-layer typewriter: protocol draft may already be complete while
 * visible text catches up by Unicode code points.
 */

export function createTypewriterState(target = "", visible = "") {
  return Object.freeze({ target, visible });
}

export function setTypewriterTarget(state, target) {
  const nextTarget = typeof target === "string" ? target : "";
  if (nextTarget === "") {
    return createTypewriterState("", "");
  }
  const targetPoints = [...nextTarget];
  const visiblePoints = [...state.visible];
  let shared = 0;
  while (
    shared < targetPoints.length
    && shared < visiblePoints.length
    && targetPoints[shared] === visiblePoints[shared]
  ) {
    shared += 1;
  }
  return createTypewriterState(nextTarget, targetPoints.slice(0, shared).join(""));
}

export function tickTypewriter(state, charsPerTick = 1) {
  const step = Number.isFinite(charsPerTick) && charsPerTick > 0
    ? Math.floor(charsPerTick)
    : 1;
  if (state.visible === state.target) {
    return state;
  }
  const targetPoints = [...state.target];
  const visiblePoints = [...state.visible];
  const nextLen = Math.min(targetPoints.length, visiblePoints.length + step);
  return createTypewriterState(state.target, targetPoints.slice(0, nextLen).join(""));
}

export function flushTypewriter(state) {
  if (state.visible === state.target) {
    return state;
  }
  return createTypewriterState(state.target, state.target);
}

export function isTypewriterCaughtUp(state) {
  return state.visible === state.target;
}

/**
 * Show partial bubble while target is non-empty and either protocol draft
 * is still present or visible text has not caught up yet.
 */
export function typewriterPartialActive(state, draftActive) {
  if (state.target.length === 0) {
    return false;
  }
  if (draftActive) {
    return true;
  }
  return !isTypewriterCaughtUp(state);
}

/**
 * When draft cleared but linger typing matches last assistant transcript
 * entry, hold that entry back to avoid duplicate full+partial display.
 */
export function holdBackMatchingAssistant(transcript, lingerTarget, draftActive, caughtUp) {
  if (draftActive || caughtUp || lingerTarget.length === 0 || transcript.length === 0) {
    return transcript;
  }
  const last = transcript[transcript.length - 1];
  if (last?.role === "assistant" && last.text === lingerTarget) {
    return transcript.slice(0, -1);
  }
  return transcript;
}
