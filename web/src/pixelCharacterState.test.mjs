import test from "node:test";
import assert from "node:assert/strict";

import {
  PIXEL_STATE_IMAGE_DISPLACEMENT_PX,
  canRunPixelAutonomy,
  createPixelCharacterState,
  reducePixelCharacterState,
  shouldApplyReplyVisualState,
} from "./pixelCharacterState.mjs";

function context(overrides = {}) {
  return {
    availableStates: ["idle", "listen", "thinking", "talk", "happy", "sad", "dragged"],
    chatOpen: false,
    dragging: false,
    petVisible: true,
    reducedMotion: false,
    sessionState: "idle",
    settingsOpen: false,
    submitting: false,
    ...overrides,
  };
}

function change(state, overrides) {
  return reducePixelCharacterState(state, {
    type: "context_changed",
    context: context(overrides),
  });
}

test("dialogue lifecycle keeps the current image while waiting", () => {
  let state = createPixelCharacterState(context());
  state = change(state, { submitting: true });
  assert.equal(state.visualState, "idle");
  state = change(state, { sessionState: "interpreting" });
  assert.equal(state.visualState, "idle");
  state = change(state, { sessionState: "planning" });
  assert.equal(state.visualState, "idle");
  state = change(state, { sessionState: "responding" });
  assert.equal(state.visualState, "idle");
  state = change(state, { sessionState: "completed" });
  assert.equal(state.visualState, "idle");
});

test("dragging preempts the current image and release restores it", () => {
  let state = createPixelCharacterState(context());
  state = reducePixelCharacterState(state, { type: "visual_state_changed", visualState: "happy" });
  state = change(state, { sessionState: "responding" });
  assert.equal(state.visualState, "happy");
  state = change(state, { sessionState: "responding", dragging: true });
  assert.equal(state.visualState, "dragged");
  state = change(state, { sessionState: "responding", dragging: false });
  assert.equal(state.visualState, "happy");
});

test("state image autonomy is disabled and displacement stays zero", () => {
  let state = createPixelCharacterState(context());
  state = reducePixelCharacterState(state, {
    type: "idle_started",
    action: "idle",
    direction: "left",
  });
  assert.equal(state.visualState, "idle");
  assert.equal(state.displacement, PIXEL_STATE_IMAGE_DISPLACEMENT_PX);
  assert.equal(canRunPixelAutonomy(state.context), false);

  state = change(state, { chatOpen: true });
  assert.equal(state.visualState, "idle");
  assert.equal(state.idleAction, "idle");
  assert.equal(state.displacement, 0);
});

test("settings hidden and reduced motion stop positional movement", () => {
  for (const blocked of [
    { settingsOpen: true },
    { petVisible: false },
    { dragging: true },
  ]) {
    const state = createPixelCharacterState(context(blocked));
    const unchanged = reducePixelCharacterState(state, {
      type: "idle_started",
      action: "idle",
      direction: "right",
    });
    assert.equal(unchanged, state);
    assert.equal(unchanged.displacement, 0);
  }

  let reduced = createPixelCharacterState(context({ reducedMotion: true }));
  reduced = reducePixelCharacterState(reduced, {
    type: "idle_started",
    action: "idle",
    direction: "right",
  });
  assert.equal(reduced.visualState, "idle");
  assert.equal(reduced.displacement, 0);
});

test("explicit emotion remains visible during active dialogue", () => {
  let state = createPixelCharacterState(context());
  state = reducePixelCharacterState(state, { type: "emotion_changed", emotion: "happy" });
  assert.equal(state.visualState, "happy");
  state = change(state, { sessionState: "responding" });
  assert.equal(state.visualState, "happy");
  state = change(state, { sessionState: "completed" });
  assert.equal(state.visualState, "happy");
  state = reducePixelCharacterState(state, { type: "emotion_changed", emotion: "neutral" });
  assert.equal(state.visualState, "idle");
});

test("missing optional state falls back to idle", () => {
  let state = createPixelCharacterState(context({ availableStates: ["idle"] }));
  state = change(state, { availableStates: ["idle"], sessionState: "responding" });
  assert.equal(state.visualState, "idle");
  state = reducePixelCharacterState(state, { type: "emotion_changed", emotion: "happy" });
  assert.equal(state.visualState, "idle");
});

test("completed visual state routes only when declared", () => {
  let state = createPixelCharacterState(context());
  state = reducePixelCharacterState(state, { type: "visual_state_changed", visualState: "happy" });
  assert.equal(state.visualState, "happy");

  state = change(state, { availableStates: ["idle"], sessionState: "completed" });
  assert.equal(state.visualState, "idle");

  state = change(state, { availableStates: ["idle", "happy"], sessionState: "responding" });
  assert.equal(state.visualState, "happy");
  state = change(state, { availableStates: ["idle", "happy"], sessionState: "completed" });
  assert.equal(state.visualState, "happy");
});

test("reply visual state is applied only after completion", () => {
  assert.equal(
    shouldApplyReplyVisualState({
      payload: {
        type: "reply_chain",
        visualState: "thinking",
      },
    }),
    false,
  );
  assert.equal(
    shouldApplyReplyVisualState({
      payload: {
        type: "completed",
        visualState: "thinking",
      },
    }),
    true,
  );
});

test("state and actions reject ambiguous input", () => {
  const state = createPixelCharacterState(context());
  assert.throws(
    () => createPixelCharacterState({ ...context(), extra: true }),
    /invalid field set/,
  );
  assert.throws(
    () => reducePixelCharacterState(state, { type: "idle_started", action: "walk" }),
    /unsupported/,
  );
  assert.throws(
    () => reducePixelCharacterState(state, { type: "emotion_changed", emotion: "angry" }),
    /unsupported/,
  );
  assert.throws(
    () => reducePixelCharacterState(state, { type: "visual_state_changed", visualState: "" }),
    /visual state/,
  );
});
