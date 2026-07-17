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

  assert.match(companionCss, /\.fairy-chat-trigger\.rt-Button\s*\{[\s\S]*left:\s*12px/);
  assert.match(companionCss, /\.fairy-chat-trigger\.rt-Button\s*\{[\s\S]*bottom:\s*42px/);
  assert.match(companionCss, /\.fairy-chat-trigger\.rt-Button::after/);
  assert.match(companionCss, /box-shadow:\s*0 2px 7px rgb\(38 106 135 \/ 10%\)/);
  assert.match(companionCss, /\.fairy-chat-card\.rt-Card::before,[\s\S]*\.fairy-chat-card\.rt-Card::after\s*\{\s*display:\s*none/);
  assert.match(companionCss, /\.fairy-chat-card__tail\s*\{[\s\S]*right:\s*-7px/);
  assert.match(companionCss, /\.fairy-chat-card__tail\s*\{[\s\S]*bottom:\s*56px/);
  assert.match(companionCss, /--fairy-chat-glass-top:\s*rgb\(250 253 254 \/ 98%\)/);
  assert.match(companionCss, /--fairy-chat-glass-bottom:\s*rgb\(244 250 252 \/ 97%\)/);
  assert.match(companionCss, /--fairy-chat-paper:\s*rgb\(255 255 255 \/ 99%\)/);
  assert.match(companionCss, /--fairy-chat-user:\s*rgb\(222 244 248 \/ 99%\)/);
  assert.match(companionCss, /--fairy-chat-accent:\s*#0f8fa6/);
  assert.match(companionCss, /\.fairy-chat-card\.rt-Card\s*\{[\s\S]*background-color:\s*rgb\(247 252 253 \/ 98%\)/);
  assert.match(companionCss, /\.fairy-chat-card\.rt-Card\s*\{[\s\S]*background-image:\s*linear-gradient\(180deg, var\(--fairy-chat-glass-top\), var\(--fairy-chat-glass-bottom\)\)/);
  assert.match(companionCss, /\.fairy-chat-card\.rt-Card\s*\{[\s\S]*backdrop-filter:\s*none/);
  assert.match(companionCss, /\.fairy-chat-card__tail\s*\{[\s\S]*background:\s*rgb\(244 250 252 \/ 97%\)/);
  assert.match(companionCss, /\.fairy-transcript\.rt-ScrollAreaRoot\s*\{[\s\S]*background:\s*linear-gradient\(180deg, rgb\(250 253 254 \/ 72%\), rgb\(247 252 253 \/ 56%\)\)/);
  assert.match(companionCss, /\.fairy-message p\s*\{[\s\S]*background-color:\s*rgb\(255 255 255 \/ 99%\)/);
  assert.match(companionCss, /\.fairy-message--user p\s*\{[\s\S]*background-color:\s*rgb\(222 244 248 \/ 99%\)/);
  assert.match(companionCss, /\.fairy-composer\s*\{[\s\S]*border-top:\s*1px solid rgb\(173 196 207 \/ 22%\)/);
  assert.match(companionCss, /\.fairy-composer__input\.rt-TextAreaRoot\s*\{[\s\S]*min-height:\s*40px/);
  assert.match(companionCss, /\.fairy-composer__input\.rt-TextAreaRoot\s*\{[\s\S]*background:\s*rgb\(255 255 255 \/ 99%\)/);
  assert.match(companionCss, /\.fairy-composer__input\.rt-TextAreaRoot textarea\s*\{[\s\S]*min-height:\s*26px/);
  assert.match(companionCss, /\.fairy-composer__actions \.rt-IconButton\[type="submit"\]:not\(:disabled\)\s*\{[\s\S]*background:\s*var\(--fairy-chat-accent\)/);
  assert.doesNotMatch(companionCss, /\.fairy-composer__input\.rt-TextAreaRoot\s*\{[\s\S]*min-height:\s*62px/);
  assert.doesNotMatch(companionCss, /\.fairy-message--user p\s*\{[\s\S]*background:\s*var\(--fairy-accent-strong\)/);
  assert.doesNotMatch(companionCss, /\.fairy-message--user p\s*\{[\s\S]*background:\s*var\(--fairy-chat-ink\)/);
  assert.doesNotMatch(companionCss, /\.fairy-message--user p\s*\{[\s\S]*background:\s*rgb\(208 238 244 \/ 38%\)/);
  assert.doesNotMatch(companionCss, /\.fairy-composer__input\.rt-TextAreaRoot\s*\{[\s\S]*background:\s*rgb\(255 255 255 \/ 38%\)/);
  assert.doesNotMatch(companionCss, /radial-gradient\(circle at 18% 0%/);
  assert.doesNotMatch(companionCss, /backdrop-filter:\s*blur\(9px\) saturate\(0\.96\)/);
  assert.doesNotMatch(companionCss, /backdrop-filter:\s*blur\(18px\) saturate\(1\.04\)/);
  assert.doesNotMatch(companionCss, /#314453|#edf9fd|#355d72/);
  assert.doesNotMatch(companionCss, /\.fairy-chat-card__header\s*\{/);
  assert.doesNotMatch(companionCss, /left:\s*50%/);
  assert.doesNotMatch(companionCss, /transform:\s*translateX\(-50%\)/);
  assert.doesNotMatch(companionCss, /box-shadow:\s*0 9px 26px/);
});
