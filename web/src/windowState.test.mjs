import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  configurationRefreshTarget,
  parseConfigurationChange,
  parseProductWindowLabel,
  selectRecentTranscript,
} from "./windowState.mjs";

test("only the two product window labels are accepted", () => {
  assert.equal(parseProductWindowLabel("companion"), "companion");
  assert.equal(parseProductWindowLabel("control-panel"), "control-panel");
  assert.throws(() => parseProductWindowLabel("main"), /unsupported/);
});

test("Wails product window label is driven by surface query", () => {
  const source = readFileSync(new URL("./windowClient.js", import.meta.url), "utf8");
  assert.match(source, /surface=control-panel|get\("surface"\)/);
  assert.match(source, /control-panel/);
  assert.doesNotMatch(
    source,
    /isWailsRuntime\(\) \{\s*return "companion";/,
  );
});

test("recent transcript projection keeps the full source untouched", () => {
  const transcript = Object.freeze(
    Array.from({ length: 15 }, (_, index) => Object.freeze({ text: `${index + 1}` })),
  );
  assert.deepEqual(selectRecentTranscript(transcript, 4).map((item) => item.text), [
    "12",
    "13",
    "14",
    "15",
  ]);
  assert.equal(transcript.length, 15);
  assert.throws(() => selectRecentTranscript(transcript, 12), /must be 4/);
});

test("configuration events accept only public invalidation payloads", () => {
  assert.deepEqual(parseConfigurationChange({ category: "model", configured: true, ready: true }), {
    category: "model",
    configured: true,
    ready: true,
  });
  assert.throws(
    () => parseConfigurationChange({ category: "model", configured: true, ready: true, apiKey: "x" }),
    /invalid field set/,
  );
  assert.throws(
    () => parseConfigurationChange({ category: "search", configured: true, ready: false }),
    /unsupported configuration change category/,
  );
});

test("configuration refresh is scoped and never clears the companion session", () => {
  assert.equal(configurationRefreshTarget({ category: "character", revision: 2 }), "character");
  assert.equal(
    configurationRefreshTarget({ category: "model", configured: false, ready: false }),
    "model",
  );
  assert.equal(configurationRefreshTarget({ category: "user_profile", revision: 3 }), null);
  assert.throws(
    () => configurationRefreshTarget({ category: "unknown" }),
    /unsupported configuration refresh category/,
  );

  const appSource = readFileSync(new URL("./App.jsx", import.meta.url), "utf8");
  assert.doesNotMatch(appSource, /session_cleared/);
});
