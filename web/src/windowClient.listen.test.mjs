import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

test("configuration and desktop listeners expose Promise-based unlisten API", () => {
  const windowSource = readFileSync(new URL("./windowClient.js", import.meta.url), "utf8");
  const desktopSource = readFileSync(new URL("./desktopClient.js", import.meta.url), "utf8");
  assert.match(windowSource, /export async function listenToConfigurationChanges/);
  assert.match(desktopSource, /export async function listenToDesktopState/);
  assert.match(windowSource, /return typeof off === "function" \? off : \(\) => \{\}/);
});

test("Wails window drag relies on CSS drag and must not claim a native startDragging bridge", () => {
  const windowSource = readFileSync(new URL("./windowClient.js", import.meta.url), "utf8");
  const appSource = readFileSync(new URL("./App.jsx", import.meta.url), "utf8");
  const companionCss = readFileSync(new URL("./styles/companion.css", import.meta.url), "utf8");
  assert.match(windowSource, /--wails-draggable/);
  assert.match(windowSource, /export async function startCurrentWindowDrag\(\)/);
  assert.doesNotMatch(windowSource, /getCurrentWindow/);
  assert.match(appSource, /consumePointerEvent:\s*false/);
  assert.match(companionCss, /\.fairy-pet__character\s*\{[\s\S]*--wails-draggable:\s*drag/);
  assert.doesNotMatch(companionCss, /data-tauri-drag-region/);
});
