export function resolveChatKeyboardAction(key, shiftKey) {
  if (typeof key !== "string" || typeof shiftKey !== "boolean") {
    throw new TypeError("chat keyboard input is invalid");
  }
  if (key === "Escape") return "close";
  if (key === "Enter" && !shiftKey) return "submit";
  return "none";
}

export function isCompanionChatViewportReady(width) {
  if (typeof width !== "number" || !Number.isFinite(width) || width < 0) {
    throw new TypeError("companion viewport width is invalid");
  }
  return width >= 510;
}

export function trackControlPanelReturn(latched, phase, visible) {
  if (typeof latched !== "boolean" || typeof phase !== "string" || typeof visible !== "boolean") {
    throw new TypeError("control panel return state is invalid");
  }
  if (phase === "transitioning_to_companion") {
    return Object.freeze({ latched: true, revealPet: false });
  }
  if (phase === "companion_idle" && visible && latched) {
    return Object.freeze({ latched: false, revealPet: true });
  }
  return Object.freeze({ latched, revealPet: false });
}
