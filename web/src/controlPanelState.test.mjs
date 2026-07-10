import test from "node:test";
import assert from "node:assert/strict";

import {
  CONTROL_PANEL_SECTIONS,
  MODEL_PROTOCOL_OPTIONS,
  assertControlPanelSection,
  buildModelConnectionInput,
} from "./controlPanelState.mjs";

test("control panel exposes exactly four product sections and two protocols", () => {
  assert.deepEqual(CONTROL_PANEL_SECTIONS.map(({ id }) => id), [
    "character",
    "profile",
    "model",
    "desktop",
  ]);
  assert.deepEqual(MODEL_PROTOCOL_OPTIONS.map(({ value }) => value), [
    "chat_completions",
    "responses",
  ]);
  assert.throws(() => assertControlPanelSection("provider"), /unsupported/);
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
