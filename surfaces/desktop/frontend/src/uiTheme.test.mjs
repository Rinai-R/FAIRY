import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

import { FAIRY_MOTION_TRANSITION, FAIRY_THEME } from "./uiTheme.mjs";

test("FAIRY theme is an explicit light translucent system", () => {
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

  assert.match(companionCss, /\.fairy-foot-dock\s*\{/);
  assert.match(companionCss, /\.fairy-foot-dock\.is-visible \.fairy-foot-dock__shell/);
  assert.match(companionCss, /\.fairy-foot-dock__shell\s*\{[\s\S]*opacity:\s*0;/);
  assert.match(companionCss, /\.fairy-speech-bubble\s*\{/);
  assert.match(companionCss, /\.fairy-history-layer\s*\{/);
  assert.match(companionCss, /\.fairy-history-card\.rt-Card\s*\{/);
  assert.match(companionCss, /--fairy-chat-glass-top:\s*rgb\(252 254 255 \/ 72%\)/);
  assert.match(companionCss, /--fairy-chat-glass-bottom:\s*rgb\(241 248 250 \/ 68%\)/);
  assert.match(companionCss, /--fairy-chat-paper:\s*rgb\(255 255 255 \/ 82%\)/);
  assert.match(companionCss, /--fairy-chat-user:\s*rgb\(15 143 166 \/ 18%\)/);
  assert.match(companionCss, /--fairy-chat-accent:\s*#0f8fa6/);
  assert.match(companionCss, /\.fairy-chat-card\.rt-Card\s*\{[\s\S]*backdrop-filter:\s*blur\(22px\) saturate\(1\.08\)/);
  assert.match(companionCss, /\.fairy-chat-card\.rt-Card\s*\{[\s\S]*-webkit-backdrop-filter:\s*blur\(22px\) saturate\(1\.08\)/);
  assert.match(companionCss, /\.fairy-message p\s*\{[\s\S]*background-color:\s*var\(--fairy-chat-paper\)/);
  assert.match(companionCss, /\.fairy-message--user p\s*\{[\s\S]*background-color:\s*var\(--fairy-chat-user\)/);
  assert.doesNotMatch(companionCss, /\.fairy-chat-card__header\s*\{/);
  assert.doesNotMatch(companionCss, /left:\s*50%/);
  assert.doesNotMatch(companionCss, /transform:\s*translateX\(-50%\)/);
  assert.doesNotMatch(companionCss, /box-shadow:\s*0 9px 26px/);
  assert.doesNotMatch(companionCss, /#314453|#edf9fd|#355d72/);
});
