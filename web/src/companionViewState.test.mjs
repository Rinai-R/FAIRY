import assert from "node:assert/strict";
import test from "node:test";

import {
  isCompanionChatViewportReady,
  resolveChatKeyboardAction,
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
  assert.equal(isCompanionChatViewportReady(176), false);
  assert.equal(isCompanionChatViewportReady(509.99), false);
  assert.equal(isCompanionChatViewportReady(510), true);
  assert.equal(isCompanionChatViewportReady(520), true);
  assert.throws(() => isCompanionChatViewportReady(Number.NaN), /invalid/);
});

test("control panel return survives batched desktop lifecycle events", () => {
  const transitioning = trackControlPanelReturn(false, "transitioning_to_companion", false);
  assert.deepEqual(transitioning, { latched: true, revealPet: false });

  const restored = trackControlPanelReturn(
    transitioning.latched,
    "companion_idle",
    true,
  );
  assert.deepEqual(restored, { latched: false, revealPet: true });
  assert.deepEqual(trackControlPanelReturn(false, "companion_idle", true), {
    latched: false,
    revealPet: false,
  });
  assert.throws(() => trackControlPanelReturn(false, null, true), /invalid/);
});
