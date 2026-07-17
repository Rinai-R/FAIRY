import assert from "node:assert/strict";
import test from "node:test";

import {
  isCompanionChatViewportReady,
  isCompanionPetDragTarget,
  resolvePixelCharacterRenderKey,
  resolveChatKeyboardAction,
  shouldDeferPixelCharacterCommit,
  trackControlPanelReturn,
} from "./companionViewState.mjs";

test("chat keyboard maps Escape to close and plain Enter to submit", () => {
  assert.equal(resolveChatKeyboardAction("Escape", false), "close");
  assert.equal(resolveChatKeyboardAction("Enter", false), "submit");
  assert.equal(resolveChatKeyboardAction("Enter", true), "none");
  assert.equal(resolveChatKeyboardAction("a", false), "none");
});

test("chat keyboard input is strict", () => {
  assert.throws(() => resolveChatKeyboardAction(null, false), /invalid/);
  assert.throws(() => resolveChatKeyboardAction("Enter", null), /invalid/);
});

test("chat popover waits for the expanded native viewport", () => {
  assert.equal(isCompanionChatViewportReady(220), false);
  assert.equal(isCompanionChatViewportReady(541.99), false);
  assert.equal(isCompanionChatViewportReady(542), true);
  assert.equal(isCompanionChatViewportReady(552), true);
  assert.throws(() => isCompanionChatViewportReady(Number.NaN), /invalid/);
});

test("chat popover preserves pointer interactions from the pet drag region", () => {
  const dragTarget = {
    closest: (selector) => selector === "[data-fairy-pet-drag-region]" ? {} : null,
  };
  const ordinaryTarget = { closest: () => null };

  assert.equal(isCompanionPetDragTarget(dragTarget), true);
  assert.equal(isCompanionPetDragTarget(ordinaryTarget), false);
  assert.equal(isCompanionPetDragTarget({}), false);
  assert.equal(isCompanionPetDragTarget(null), false);
});

test("control panel return latches on settings visible and survives restore events", () => {
  const settingsOpen = trackControlPanelReturn(false, "control_panel_visible", false);
  assert.deepEqual(settingsOpen, { latched: true, revealPet: false });

  const transitioning = trackControlPanelReturn(
    settingsOpen.latched,
    "transitioning_to_companion",
    false,
  );
  assert.deepEqual(transitioning, { latched: true, revealPet: false });

  const restored = trackControlPanelReturn(
    transitioning.latched,
    "companion_idle",
    true,
  );
  assert.deepEqual(restored, { latched: false, revealPet: true });

  // Even if the transitioning event is dropped, latch from settings is enough.
  assert.deepEqual(
    trackControlPanelReturn(true, "companion_idle", true),
    { latched: false, revealPet: true },
  );
  assert.deepEqual(trackControlPanelReturn(false, "companion_idle", true), {
    latched: false,
    revealPet: false,
  });
  assert.throws(() => trackControlPanelReturn(false, null, true), /invalid/);
});

test("pixel character commit is deferred while companion window is hidden", () => {
  assert.equal(
    shouldDeferPixelCharacterCommit({ desktopVisible: true, controlPanelVisible: false }),
    false,
  );
  assert.equal(
    shouldDeferPixelCharacterCommit({ desktopVisible: false, controlPanelVisible: true }),
    true,
  );
  assert.equal(
    shouldDeferPixelCharacterCommit({ desktopVisible: true, controlPanelVisible: true }),
    true,
  );
  assert.equal(
    shouldDeferPixelCharacterCommit({ desktopVisible: false, controlPanelVisible: false }),
    true,
  );
  assert.throws(
    () => shouldDeferPixelCharacterCommit({ desktopVisible: null, controlPanelVisible: false }),
    /invalid/,
  );
});

test("companion app defers pixel commit until control panel returns", async () => {
  const { readFileSync } = await import("node:fs");
  const appSource = readFileSync(new URL("./App.jsx", import.meta.url), "utf8");
  assert.match(appSource, /shouldDeferPixelCharacterCommit/);
  assert.match(appSource, /pixelSurfaceEpoch/);
  assert.match(appSource, /displayCharacter/);
  assert.match(appSource, /displayVisual/);
  assert.doesNotMatch(appSource, /mountPixelSurface/);
  assert.doesNotMatch(appSource, /shouldMountPixelCharacterSurface/);
});

test("pixel character render key changes when active character changes on the same visual pack", () => {
  const sharedVisual = { packId: "fairy.atri" };
  const first = {
    characterId: "11111111-1111-4111-8111-111111111111",
    appearance: { status: "assigned", bindingRevision: 1, visual: sharedVisual },
  };
  const second = {
    characterId: "22222222-2222-4222-8222-222222222222",
    appearance: { status: "assigned", bindingRevision: 1, visual: sharedVisual },
  };

  assert.notEqual(
    resolvePixelCharacterRenderKey(first, sharedVisual),
    resolvePixelCharacterRenderKey(second, sharedVisual),
  );
  assert.notEqual(
    resolvePixelCharacterRenderKey(first, sharedVisual),
    resolvePixelCharacterRenderKey({
      ...first,
      appearance: { status: "assigned", bindingRevision: 2, visual: sharedVisual },
    }, sharedVisual),
  );
  assert.equal(resolvePixelCharacterRenderKey(null, sharedVisual), null);
  assert.equal(resolvePixelCharacterRenderKey(first, null), null);
  assert.throws(() => resolvePixelCharacterRenderKey({ appearance: first.appearance }, sharedVisual), /characterId/);
});
