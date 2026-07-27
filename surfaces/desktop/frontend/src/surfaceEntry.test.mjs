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

test("Core connection settings stay in the Go backend rather than WebView storage", () => {
  assert.match(surfaceSource, /ConnectionSettings\(\)/);
  assert.match(surfaceSource, /Connect\(\)/);
  assert.doesNotMatch(surfaceSource, /Keychain|keychain/);
  assert.doesNotMatch(surfaceSource, /fairy\.endpoint(?:Key)?/);
  assert.doesNotMatch(surfaceSource, /localStorage\.(?:getItem|setItem)\([^)]*(?:endpoint|token)/);
});
