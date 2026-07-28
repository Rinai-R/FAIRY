import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

const mainSource = readFileSync(new URL("./main.jsx", import.meta.url), "utf8");
const surfaceSource = readFileSync(new URL("./surface.jsx", import.meta.url), "utf8");

test("production entry uses the standalone CoreService surface router", () => {
  assert.match(mainSource, /import \{ SurfaceApp \} from "\.\/surface\.jsx"/);
  assert.match(surfaceSource, /\.\.\/bindings\/fairy-desktop\/coreservice\.js/);
  assert.match(surfaceSource, /if \(surface === "control-panel"\)/);
  assert.match(surfaceSource, /if \(surface === "history"\)/);
  assert.match(surfaceSource, /if \(surface === "speech"\)/);
  assert.doesNotMatch(mainSource + surfaceSource, /fairy\/frontend\/bindings\/fairy\/desktop/);
  assert.doesNotMatch(mainSource + surfaceSource, /GetDesktopState|OpenCompanionChat/);
});

test("Desktop requests Core speech and wires paired audio into the playback owner", () => {
  assert.match(surfaceSource, /Send\(input, true\)/);
  assert.doesNotMatch(surfaceSource, /Send\(input, false\)/);
  assert.match(surfaceSource, /createSpeechPlayback/);
  assert.match(surfaceSource, /player\.enqueue\(turn\.turnId \|\| "", turn\.beat\)/);
  assert.match(surfaceSource, /const newIdentifiedTurn = nextTurnId && currentPlaybackTurn !== nextTurnId/);
  assert.match(surfaceSource, /if \(newIdentifiedTurn \|\| localPendingTurn\)/);
  assert.match(surfaceSource, /player\.beginTurn\(nextTurnId\)/);
  assert.match(surfaceSource, /player\.stop\(\)/);
});

test("speech surface bounds sticker delivery receipt deduplication", () => {
  assert.match(surfaceSource, /MAX_REPORTED_STICKER_RECEIPTS = 16/);
  assert.match(surfaceSource, /reportedStickersRef\.current\.delete\(oldest\)/);
});

test("companion surface projects direct and proactive Turn events into one active state", () => {
  assert.match(surfaceSource, /import \{ projectDesktopTurnActive \} from "\.\/turnViewState\.mjs"/);
  assert.match(surfaceSource, /setActive\(\(current\) => projectDesktopTurnActive\(current, turn\)\)/);
  assert.match(surfaceSource, /disabled=\{!session \|\| active\}/);
  assert.match(surfaceSource, /active \? <IconButton[^>]+aria-label="停止回复"/);
});

test("Core connection settings stay in the Go backend rather than WebView storage", () => {
  assert.match(surfaceSource, /ConnectionSettings\(\)/);
  assert.match(surfaceSource, /Connect\(\)/);
  assert.doesNotMatch(surfaceSource, /Keychain|keychain/);
  assert.doesNotMatch(surfaceSource, /fairy\.endpoint(?:Key)?/);
  assert.doesNotMatch(surfaceSource, /localStorage\.(?:getItem|setItem)\([^)]*(?:endpoint|token)/);
});
