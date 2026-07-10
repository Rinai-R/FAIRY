import assert from "node:assert/strict";
import test from "node:test";

import { FAIRY_MOTION_TRANSITION, FAIRY_THEME } from "./uiTheme.mjs";

test("FAIRY theme is an explicit light Swiss white-blue system", () => {
  assert.deepEqual(FAIRY_THEME, {
    appearance: "light",
    accentColor: "sky",
    grayColor: "slate",
    panelBackground: "translucent",
    radius: "large",
    scaling: "100%",
    hasBackground: false,
  });
  assert.equal(Object.isFrozen(FAIRY_THEME), true);
});

test("surface motion uses the approved short transform curve", () => {
  assert.equal(FAIRY_MOTION_TRANSITION.duration, 0.28);
  assert.deepEqual(FAIRY_MOTION_TRANSITION.ease, [0.16, 1, 0.3, 1]);
  assert.equal(Object.isFrozen(FAIRY_MOTION_TRANSITION), true);
  assert.equal(Object.isFrozen(FAIRY_MOTION_TRANSITION.ease), true);
});
