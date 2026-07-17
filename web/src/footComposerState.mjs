/**
 * Foot composer visibility: hidden by default, shown on foot hover or input focus,
 * forced hidden while dragging the pet.
 */

export function createFootComposerUiState() {
  return Object.freeze({
    pointerInside: false,
    focused: false,
    dragging: false,
  });
}

export function reduceFootComposerUiState(state, action) {
  if (state === null || typeof state !== "object" || Object.isFrozen(state) === false) {
    throw new TypeError("foot composer state must be immutable");
  }
  if (action === null || typeof action !== "object" || Array.isArray(action)) {
    throw new TypeError("foot composer action must be an object");
  }
  switch (action.type) {
    case "pointer_enter":
      if (state.dragging) return state;
      return Object.freeze({ ...state, pointerInside: true });
    case "pointer_leave":
      return Object.freeze({ ...state, pointerInside: false });
    case "focus":
      if (state.dragging) return state;
      return Object.freeze({ ...state, focused: true });
    case "blur":
      return Object.freeze({ ...state, focused: false });
    case "drag_start":
      return Object.freeze({
        pointerInside: false,
        focused: false,
        dragging: true,
      });
    case "drag_end":
      return Object.freeze({ ...state, dragging: false });
    default:
      throw new TypeError(`unsupported foot composer action: ${String(action.type)}`);
  }
}

export function isFootComposerVisible(state) {
  if (state.dragging) return false;
  return state.pointerInside || state.focused;
}
