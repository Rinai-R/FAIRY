import assert from "node:assert/strict";
import test from "node:test";

import { resolveChatKeyboardAction } from "./companionViewState.mjs";

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
