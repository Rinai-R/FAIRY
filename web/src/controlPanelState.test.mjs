import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  CONTROL_PANEL_SECTIONS,
  MODEL_PROTOCOL_OPTIONS,
  assertControlPanelSection,
  buildModelConnectionInput,
  buildCharacterSaveInput,
  controlPanelCharacterPreviewUrl,
  controlPanelVisualPreviewUrl,
  selectedAppearancePackId,
} from "./controlPanelState.mjs";

const ATRI_VISUAL = Object.freeze({
  schemaVersion: 2,
  packId: "fairy.atri",
  displayName: "亚托莉",
  renderer: "state_images",
  frame: Object.freeze({ width: 128, height: 192 }),
  scale: 1,
  anchor: Object.freeze({ x: 64, y: 190 }),
  states: Object.freeze([
    Object.freeze({
      id: "idle",
      description: "安静站立",
      imagePath: "fairy-character://localhost/fairy.atri/images/idle.png",
    }),
  ]),
});

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

test("character package import and export use local file dialogs", () => {
  const appSource = readFileSync(new URL("./apps/ControlPanelApp.jsx", import.meta.url), "utf8");
  const dialogSource = readFileSync(new URL("./fileDialogClient.mjs", import.meta.url), "utf8");
  const cssSource = readFileSync(new URL("./styles/control-panel.css", import.meta.url), "utf8");

  assert.match(dialogSource, /@wailsio\/runtime/);
  assert.match(dialogSource, /Pattern: "\*\.pack;?\*\.zip"/);
  assert.match(dialogSource, /selectCharacterPackageSavePath/);
  assert.match(dialogSource, /Dialogs\.SaveFile/);
  assert.match(appSource, /character-package-dropped/);
  assert.match(appSource, /className=\{`cp-package-dropzone/);
  assert.match(appSource, /selectCharacterPackageFile\(\)/);
  assert.match(appSource, /importActiveCharacterPackage\(packagePath\)/);
  assert.match(appSource, /selectCharacterPackageSavePath\(selectedCharacter\.name\)/);
  assert.doesNotMatch(appSource, /character-package-path/);
  assert.doesNotMatch(appSource, /character-package-export-path/);
  assert.doesNotMatch(appSource, /getCurrentWebview\(\)\.onDragDropEvent/);
  assert.match(appSource, /图片会复制到本地库，不进入 Git/);
  assert.match(cssSource, /\.cp-import-card\.rt-Card/);
  assert.match(cssSource, /\.cp-package-dropzone\.is-dragging/);
});

test("character management surfaces avoid translucent stacked panels", () => {
  const cssSource = readFileSync(new URL("./styles/control-panel.css", import.meta.url), "utf8");
  const transparentPanelPatterns = [
    /rgb\(255 255 255 \//,
    /rgb\(247 251 253 \//,
    /linear-gradient\(180deg/,
    /radial-gradient/,
    /backdrop-filter:\s*blur/,
  ];

  for (const pattern of transparentPanelPatterns) {
    assert.doesNotMatch(cssSource, pattern);
  }
  assert.match(cssSource, /\.cp-shell\.rt-Card\s*\{[\s\S]*background: #ffffff/);
  assert.match(cssSource, /\.cp-character-stage-card\s*\{[\s\S]*background: #ffffff/);
  assert.match(cssSource, /\.cp-character-dialogue\s*\{[\s\S]*background: #ffffff/);
  assert.match(cssSource, /\.cp-package-dropzone\s*\{[\s\S]*background: #f7fbfd/);
});

test("appearance picker uses a bounded popper menu", () => {
  const appSource = readFileSync(new URL("./apps/ControlPanelApp.jsx", import.meta.url), "utf8");
  const cssSource = readFileSync(new URL("./styles/control-panel.css", import.meta.url), "utf8");

  assert.match(appSource, /className="cp-appearance-select-content" position="popper"/);
  assert.match(cssSource, /\.cp-appearance-select-content\.rt-SelectContent:where\(\[data-side\]\)\s*\{/);
  assert.match(cssSource, /max-height: min\(184px, var\(--radix-select-content-available-height\)\)/);
  assert.match(cssSource, /\.cp-appearance-picker\s*\{[\s\S]*align-items: start/);
  assert.match(appSource, /controlPanelVisualPreviewUrl/);
  assert.match(appSource, /controlPanelCharacterPreviewUrl/);
  assert.match(appSource, /value=\{visualPackId \|\| undefined\}/);
});

test("character page scroll path avoids transform and filter flicker sources", () => {
  const appSource = readFileSync(new URL("./apps/ControlPanelApp.jsx", import.meta.url), "utf8");
  const cssSource = readFileSync(new URL("./styles/control-panel.css", import.meta.url), "utf8");

  assert.match(appSource, /initial=\{\{\s*opacity:\s*0\s*\}\}/);
  assert.doesNotMatch(appSource, /initial=\{\{\s*opacity:\s*0,\s*x:/);
  assert.match(cssSource, /\.cp-character-portrait\s*\{[\s\S]*filter:\s*none/);
  assert.match(cssSource, /\.cp-scroll \.rt-ScrollAreaViewport\s*\{[\s\S]*transform:\s*none/);
});

test("control panel preview URLs rewrite fairy-character onto the Wails asset route", () => {
  const previous = globalThis.window;
  globalThis.window = { _wails: {} };
  try {
    assert.equal(
      controlPanelVisualPreviewUrl(ATRI_VISUAL, "http://wails.localhost"),
      "http://wails.localhost/fairy-character/fairy.atri/images/idle.png",
    );
    assert.equal(
      controlPanelCharacterPreviewUrl(
        { appearance: { status: "assigned", visual: ATRI_VISUAL } },
        "http://wails.localhost",
      ),
      "http://wails.localhost/fairy-character/fairy.atri/images/idle.png",
    );
    assert.equal(
      controlPanelCharacterPreviewUrl(
        { appearance: { status: "unassigned" } },
        "http://wails.localhost",
      ),
      "",
    );
    assert.equal(controlPanelVisualPreviewUrl(null, "http://wails.localhost"), "");
    assert.equal(controlPanelVisualPreviewUrl(ATRI_VISUAL, ""), "");
  } finally {
    if (previous === undefined) delete globalThis.window;
    else globalThis.window = previous;
  }
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
