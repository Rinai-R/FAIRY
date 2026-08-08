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

test("Desktop sends text turns and renders ordered display beats", () => {
  assert.match(surfaceSource, /Send\(input\)/);
  assert.doesNotMatch(surfaceSource, /createSpeechPlayback|new Audio|dataUrl|audioUnavailable/);
  assert.match(surfaceSource, /const newIdentifiedTurn = nextTurnId && bubbleRef\.current\.turnId !== nextTurnId/);
  assert.match(surfaceSource, /if \(newIdentifiedTurn \|\| localPendingTurn\)/);
  assert.match(surfaceSource, /if \(newIdentifiedTurn \|\| localPendingTurn\) \{\s+return waitingPhase\s+\? \{ visible: true, waiting: true, settled: false, turnId: nextTurnId, parts: \[\] \}/);
  assert.match(surfaceSource, /turn\?\.state === "interrupted"/);
  assert.match(surfaceSource, /if \(isDesktopTurnAborted\(turn\)\) \{/);
  const abortBranch = surfaceSource.indexOf("if (isDesktopTurnAborted(turn)) {");
  const genericStateBranch = surfaceSource.indexOf('if (turn.type === "state_changed") {', abortBranch);
  assert.ok(abortBranch >= 0 && genericStateBranch > abortBranch);
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
  assert.match(surfaceSource, />保存连接配置<\/button>/);
  assert.match(surfaceSource, /重启后将自动连接/);
  assert.doesNotMatch(surfaceSource, /保存并连接/);
  const saveStart = surfaceSource.indexOf("async function save(event)");
  const saveEnd = surfaceSource.indexOf("async function applyObservation()", saveStart);
  assert.ok(saveStart >= 0 && saveEnd > saveStart);
  assert.doesNotMatch(surfaceSource.slice(saveStart, saveEnd), /\bConnect\(\)/);
  assert.doesNotMatch(surfaceSource, /Keychain|keychain/);
  assert.doesNotMatch(surfaceSource, /fairy\.endpoint(?:Key)?/);
  assert.doesNotMatch(surfaceSource, /localStorage\.(?:getItem|setItem)\([^)]*(?:endpoint|token)/);
});
