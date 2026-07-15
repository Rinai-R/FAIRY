import test from "node:test";
import assert from "node:assert/strict";

import {
  canStartPetWindowDrag,
  startPetWindowDrag,
} from "./petDragState.mjs";

function dragEvent(button = 0) {
  const calls = [];
  return {
    button,
    calls,
    preventDefault: () => calls.push("preventDefault"),
    stopPropagation: () => calls.push("stopPropagation"),
  };
}

test("pet window drag only starts for primary button on visible ready desktop", () => {
  assert.equal(canStartPetWindowDrag({ button: 0, desktopReady: true, petVisualOpen: true }), true);
  assert.equal(canStartPetWindowDrag({ button: 1, desktopReady: true, petVisualOpen: true }), false);
  assert.equal(canStartPetWindowDrag({ button: 0, desktopReady: false, petVisualOpen: true }), false);
  assert.equal(canStartPetWindowDrag({ button: 0, desktopReady: true, petVisualOpen: false }), false);
});

test("pet window drag invokes native dragging immediately and marks dragging", async () => {
  const event = dragEvent();
  const dragging = [];
  let nativeCalls = 0;
  const started = startPetWindowDrag({
    event,
    desktopReady: true,
    petVisualOpen: true,
    startDragging: () => {
      nativeCalls += 1;
      return Promise.resolve();
    },
    setDragging: (value) => dragging.push(value),
    onError: () => assert.fail("drag should not fail"),
  });

  assert.equal(started, true);
  assert.deepEqual(event.calls, ["preventDefault", "stopPropagation"]);
  assert.deepEqual(dragging, [true]);
  assert.equal(nativeCalls, 1);
});

test("pet window drag resets dragging and reports native drag failure", async () => {
  const event = dragEvent();
  const dragging = [];
  const errors = [];
  startPetWindowDrag({
    event,
    desktopReady: true,
    petVisualOpen: true,
    startDragging: () => Promise.reject(new Error("native drag failed")),
    setDragging: (value) => dragging.push(value),
    onError: (error) => errors.push(error.message),
  });
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.deepEqual(dragging, [true, false]);
  assert.deepEqual(errors, ["native drag failed"]);
});

test("pet window drag ignores inactive cases without consuming the event", () => {
  const event = dragEvent(0);
  const started = startPetWindowDrag({
    event,
    desktopReady: false,
    petVisualOpen: true,
    startDragging: () => assert.fail("native drag must not start"),
    setDragging: () => assert.fail("dragging state must not change"),
    onError: () => assert.fail("error must not be reported"),
  });

  assert.equal(started, false);
  assert.deepEqual(event.calls, []);
});
