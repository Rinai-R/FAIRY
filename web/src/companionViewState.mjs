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
  // Native chat window is 552 logical px; wait until resize has landed.
  return width >= 542;
}

export function isCompanionPetDragTarget(target) {
  if (target === null || target === undefined || typeof target.closest !== "function") {
    return false;
  }
  return target.closest("[data-fairy-pet-drag-region]") !== null;
}

export function trackControlPanelReturn(latched, phase, visible) {
  if (typeof latched !== "boolean" || typeof phase !== "string" || typeof visible !== "boolean") {
    throw new TypeError("control panel return state is invalid");
  }
  // Latch as soon as settings is shown so restore does not depend on receiving
  // the short-lived transitioning_to_companion event.
  if (
    phase === "control_panel_visible"
    || phase === "transitioning_to_settings"
    || phase === "transitioning_to_companion"
  ) {
    return Object.freeze({ latched: true, revealPet: false });
  }
  if (phase === "companion_idle" && visible && latched) {
    return Object.freeze({ latched: false, revealPet: true });
  }
  return Object.freeze({ latched, revealPet: false });
}

export function shouldDeferPixelCharacterCommit({ desktopVisible, controlPanelVisible }) {
  if (typeof desktopVisible !== "boolean" || typeof controlPanelVisible !== "boolean") {
    throw new TypeError("pixel character commit defer state is invalid");
  }
  // Keep the currently committed Pixi identity while the companion window is
  // hidden. Committing a character switch in a hidden WKWebView creates a blank
  // WebGL canvas that stays invisible even after drag.
  return !desktopVisible || controlPanelVisible;
}

export function resolvePixelCharacterRenderKey(character, visual) {
  if (character === null || visual === null) return null;
  if (character === undefined || visual === undefined) {
    throw new TypeError("pixel character render identity is invalid");
  }
  if (
    typeof character !== "object" ||
    Array.isArray(character) ||
    typeof character.characterId !== "string" ||
    character.characterId.length === 0
  ) {
    throw new TypeError("pixel character render identity requires characterId");
  }
  if (
    typeof visual !== "object" ||
    Array.isArray(visual) ||
    typeof visual.packId !== "string" ||
    visual.packId.length === 0
  ) {
    throw new TypeError("pixel character render identity requires visual packId");
  }
  const bindingRevision = character.appearance?.status === "assigned"
    ? character.appearance.bindingRevision
    : null;
  if (
    bindingRevision !== null &&
    (
      typeof bindingRevision !== "number" ||
      !Number.isInteger(bindingRevision) ||
      bindingRevision < 1
    )
  ) {
    throw new TypeError("pixel character render identity requires a valid bindingRevision");
  }
  return [
    character.characterId,
    bindingRevision ?? "unbound",
    visual.packId,
  ].join(":");
}
