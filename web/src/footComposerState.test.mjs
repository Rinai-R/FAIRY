import assert from "node:assert/strict";
import test from "node:test";

import {
  createFootComposerUiState,
  isFootComposerVisible,
  reduceFootComposerUiState,
} from "./footComposerState.mjs";

test("foot composer reveals on hover and focus, hides when idle", () => {
  let state = createFootComposerUiState();
  assert.equal(isFootComposerVisible(state), false);

  state = reduceFootComposerUiState(state, { type: "pointer_enter" });
  assert.equal(isFootComposerVisible(state), true);

  state = reduceFootComposerUiState(state, { type: "pointer_leave" });
  assert.equal(isFootComposerVisible(state), false);

  state = reduceFootComposerUiState(state, { type: "focus" });
  assert.equal(isFootComposerVisible(state), true);

  state = reduceFootComposerUiState(state, { type: "pointer_leave" });
  assert.equal(isFootComposerVisible(state), true);

  state = reduceFootComposerUiState(state, { type: "blur" });
  assert.equal(isFootComposerVisible(state), false);
});

test("drag forces composer hidden", () => {
  let state = createFootComposerUiState();
  state = reduceFootComposerUiState(state, { type: "pointer_enter" });
  state = reduceFootComposerUiState(state, { type: "focus" });
  assert.equal(isFootComposerVisible(state), true);

  state = reduceFootComposerUiState(state, { type: "drag_start" });
  assert.equal(isFootComposerVisible(state), false);
  assert.equal(state.focused, false);

  state = reduceFootComposerUiState(state, { type: "pointer_enter" });
  assert.equal(isFootComposerVisible(state), false);

  state = reduceFootComposerUiState(state, { type: "drag_end" });
  state = reduceFootComposerUiState(state, { type: "pointer_enter" });
  assert.equal(isFootComposerVisible(state), true);
});
