import assert from "node:assert/strict";
import test from "node:test";

import { projectDesktopTurnActive } from "./turnViewState.mjs";

test("Desktop projects every nonterminal Turn state to active", () => {
  for (const state of ["interpreting", "gathering", "planning", "responding"]) {
    assert.equal(projectDesktopTurnActive(false, { type: "state_changed", state }), true, state);
  }
  assert.equal(projectDesktopTurnActive(false, { type: "beat.ready", state: "responding" }), true);
});

test("Desktop projects terminal state or stream closure to inactive", () => {
  for (const state of ["completed", "failed", "interrupted"]) {
    assert.equal(projectDesktopTurnActive(true, { type: "state_changed", state }), false, state);
  }
  for (const type of ["completed", "failed", "interrupted", "stream.closed"]) {
    assert.equal(projectDesktopTurnActive(true, { type }), false, type);
  }
});

test("Desktop preserves active state for unrelated events", () => {
  assert.equal(projectDesktopTurnActive(true, { type: "beat.ready", state: "" }), true);
  assert.equal(projectDesktopTurnActive(false, { type: "presence", state: "unknown" }), false);
  assert.equal(projectDesktopTurnActive(true, null), true);
  assert.throws(() => projectDesktopTurnActive("true", {}), /must be boolean/);
});
