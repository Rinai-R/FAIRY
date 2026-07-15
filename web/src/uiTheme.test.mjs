import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

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

test("companion idle chrome stays tight around the character", () => {
  const companionCss = readFileSync(new URL("./styles/companion.css", import.meta.url), "utf8");
  const sharedCss = readFileSync(new URL("./styles/shared.css", import.meta.url), "utf8");

  for (const source of [companionCss, sharedCss]) {
    assert.match(source, /--fairy-pet-width:\s*208px/);
    assert.match(source, /--fairy-pet-height:\s*326px/);
    assert.match(source, /--fairy-pet-stage-height:\s*326px/);
  }

  assert.match(companionCss, /\.fairy-chat-trigger\.rt-Button\s*\{[\s\S]*left:\s*12px/);
  assert.match(companionCss, /\.fairy-chat-trigger\.rt-Button\s*\{[\s\S]*bottom:\s*42px/);
  assert.match(companionCss, /\.fairy-chat-trigger\.rt-Button::after/);
  assert.match(companionCss, /box-shadow:\s*0 2px 7px rgb\(38 106 135 \/ 10%\)/);
  assert.doesNotMatch(companionCss, /left:\s*50%/);
  assert.doesNotMatch(companionCss, /transform:\s*translateX\(-50%\)/);
  assert.doesNotMatch(companionCss, /box-shadow:\s*0 9px 26px/);
});
