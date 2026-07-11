import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  CONTROL_PANEL_SECTIONS,
  MODEL_PROTOCOL_OPTIONS,
  assertControlPanelSection,
  buildModelConnectionInput,
  buildSearchConnectionInput,
} from "./controlPanelState.mjs";

test("control panel exposes five product sections and two protocols", () => {
  assert.deepEqual(CONTROL_PANEL_SECTIONS.map(({ id }) => id), [
    "character",
    "profile",
    "model",
    "intelligence",
    "desktop",
  ]);
  assert.deepEqual(MODEL_PROTOCOL_OPTIONS.map(({ value }) => value), [
    "chat_completions",
    "responses",
  ]);
  assert.throws(() => assertControlPanelSection("provider"), /unsupported/);
});

test("search form produces one explicit Brave provider without fallback fields", () => {
  const input = buildSearchConnectionInput({
    endpoint: " https://api.search.brave.com/res/v1/web/search ",
  });
  assert.deepEqual(input, {
    provider: "brave",
    endpoint: "https://api.search.brave.com/res/v1/web/search",
  });
  assert.equal("fallbackProvider" in input, false);
  assert.throws(
    () => buildSearchConnectionInput({ endpoint: "   " }),
    /endpoint is required/,
  );
});

test("model form produces the exact public input without cache switches", () => {
  const input = buildModelConnectionInput({
    protocol: "chat_completions",
    endpoint: " https://api.deepseek.com ",
    model: " deepseek-chat ",
    authMode: "bearer_key",
    promptCacheKey: false,
  });
  assert.deepEqual(input, {
    protocol: "chat_completions",
    endpoint: "https://api.deepseek.com",
    model: "deepseek-chat",
    authMode: "bearer_key",
  });
  assert.equal("promptCacheKey" in input, false);
  assert.throws(
    () => buildModelConnectionInput({ protocol: "auto", endpoint: "x", model: "m", authMode: "no_auth" }),
    /unsupported model protocol/,
  );
});

test("intelligence status layout targets only its dot and stacks on narrow windows", () => {
  const appSource = readFileSync(new URL("./apps/ControlPanelApp.jsx", import.meta.url), "utf8");
  const cssSource = readFileSync(new URL("./styles/control-panel.css", import.meta.url), "utf8");

  assert.match(appSource, /className="cp-intelligence-status-dot"/);
  assert.doesNotMatch(cssSource, /\.cp-intelligence-track\s*>\s*div\s*>\s*span/);
  assert.match(cssSource, /\.cp-intelligence-status-dot\s*\{/);
  assert.match(cssSource, /@media \(max-width: 520px\)[\s\S]*\.cp-intelligence-track\s*\{[\s\S]*grid-template-columns: minmax\(0, 1fr\)/);
});
