import test from "node:test";
import assert from "node:assert/strict";

import {
  projectCharacterMotion,
  STATIC_CHARACTER_MOTION,
} from "./characterMotion.mjs";

test("reduced motion always returns the static projection", () => {
  for (const motion of ["still", "float", "pulse", "bounce"]) {
    assert.equal(projectCharacterMotion(motion, 1234, true), STATIC_CHARACTER_MOTION);
  }
});

test("entry motion settles exactly back to the declared loop", () => {
  const start = projectCharacterMotion("still", 0);
  assert.notDeepEqual(start, STATIC_CHARACTER_MOTION);
  assert.deepEqual(projectCharacterMotion("still", 360), STATIC_CHARACTER_MOTION);
});

test("absolute-time loops repeat without accumulated drift", () => {
  const first = projectCharacterMotion("float", 1800);
  assert.deepEqual(projectCharacterMotion("float", 1800 + 3600 * 1000), first);

  const pulse = projectCharacterMotion("pulse", 1200);
  assert.deepEqual(projectCharacterMotion("pulse", 1200 + 2400 * 1000), pulse);
});

test("all motion profiles remain finite and bounded over a day", () => {
  for (const motion of ["still", "float", "pulse", "bounce"]) {
    for (let elapsed = 0; elapsed <= 86_400_000; elapsed += 137_111) {
      const frame = projectCharacterMotion(motion, elapsed);
      for (const value of Object.values(frame)) assert.equal(Number.isFinite(value), true);
      assert.ok(Math.abs(frame.offsetXRatio) <= 0.001);
      assert.ok(Math.abs(frame.offsetYRatio) <= 0.02);
      assert.ok(frame.scaleX >= 0.97 && frame.scaleX <= 1.03);
      assert.ok(frame.scaleY >= 0.97 && frame.scaleY <= 1.03);
    }
  }
});

test("invalid profile and elapsed time fail closed", () => {
  assert.throws(() => projectCharacterMotion("spin", 0), /unsupported/);
  assert.throws(() => projectCharacterMotion("still", -1), /elapsed time/);
  assert.throws(() => projectCharacterMotion("still", Number.NaN), /elapsed time/);
});
