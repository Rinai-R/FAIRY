import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  CONTROL_PANEL_SECTIONS,
  MODEL_PROTOCOL_OPTIONS,
  assertControlPanelSection,
  buildModelConnectionInput,
  buildCharacterSaveInput,
  selectedAppearancePackId,
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

test("model form produces the exact public input without cache switches", () => {
  const input = buildModelConnectionInput({
    protocol: "chat_completions",
    endpoint: " https://api.deepseek.com ",
    model: " deepseek-chat ",
    contextWindowTokens: " 128000 ",
    authMode: "bearer_key",
    promptCacheKey: false,
  });
  assert.deepEqual(input, {
    protocol: "chat_completions",
    endpoint: "https://api.deepseek.com",
    model: "deepseek-chat",
    contextWindowTokens: 128000,
    authMode: "bearer_key",
  });
  assert.equal("promptCacheKey" in input, false);
  assert.throws(
    () => buildModelConnectionInput({ protocol: "auto", endpoint: "x", model: "m", contextWindowTokens: 128000, authMode: "no_auth" }),
    /unsupported model protocol/,
  );
  assert.throws(
    () => buildModelConnectionInput({ protocol: "responses", endpoint: "x", model: "m", contextWindowTokens: 2048, authMode: "no_auth" }),
    /context window/,
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

test("control panel does not expose retired network search configuration", () => {
  const appSource = readFileSync(new URL("./apps/ControlPanelApp.jsx", import.meta.url), "utf8");

  assert.doesNotMatch(appSource, /Brave Search|Search Endpoint|搜索连接/);
});

test("appearance picker uses a bounded popper menu", () => {
  const appSource = readFileSync(new URL("./apps/ControlPanelApp.jsx", import.meta.url), "utf8");
  const cssSource = readFileSync(new URL("./styles/control-panel.css", import.meta.url), "utf8");

  assert.match(appSource, /className="cp-appearance-select-content" position="popper"/);
  assert.match(cssSource, /\.cp-appearance-select-content\.rt-SelectContent:where\(\[data-side\]\)\s*\{/);
  assert.match(cssSource, /max-height: min\(184px, var\(--radix-select-content-available-height\)\)/);
  assert.match(cssSource, /\.cp-appearance-picker\s*\{[\s\S]*align-items: start/);
});

test("character form requires an explicit visual pack without inventing a default", () => {
  assert.deepEqual(
    buildCharacterSaveInput({
      name: " 亚托莉 ",
      description: " 会认真听用户说话。 ",
      visualPackId: "fairy.atri",
    }),
    {
      brief: { name: "亚托莉", description: "会认真听用户说话。" },
      visualPackId: "fairy.atri",
    },
  );
  assert.throws(
    () => buildCharacterSaveInput({ name: "旧角色", description: "保留历史", visualPackId: "" }),
    /must be selected/,
  );
  assert.deepEqual(
    buildCharacterSaveInput({
      name: " 亚托莉 ",
      description: " 来自海边的仿生少女。 ",
      dialogueStyle: "  短句，先接住用户当下的话。  ",
      visualPackId: "fairy.atri",
    }),
    {
      brief: {
        name: "亚托莉",
        description: "来自海边的仿生少女。",
        dialogueStyle: "短句，先接住用户当下的话。",
      },
      visualPackId: "fairy.atri",
    },
  );
  assert.equal(
    "dialogueStyle" in buildCharacterSaveInput({
      name: "旧角色",
      description: "保留历史",
      dialogueStyle: "   ",
      visualPackId: "fairy.atri",
    }).brief,
    false,
  );
  assert.equal(selectedAppearancePackId(null), "");
  assert.equal(
    selectedAppearancePackId({ appearance: { status: "unassigned" } }),
    "",
  );
  assert.equal(
    selectedAppearancePackId({
      appearance: { status: "assigned", visual: { packId: "fairy.atri" } },
    }),
    "fairy.atri",
  );
});
