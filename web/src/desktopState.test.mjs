import test from "node:test";
import assert from "node:assert/strict";

import {
  normalizeInvokeError,
  parseDesktopState,
  parseHealthResponse,
} from "./desktopState.mjs";

test("parseDesktopState preserves valid false values", () => {
  const state = parseDesktopState({
    alwaysOnTop: false,
    clickThrough: false,
    trayReady: true,
    visible: false,
    companionSurface: "idle",
    controlPanelVisible: false,
    phase: "companion_idle",
  });

  assert.deepEqual(state, {
    alwaysOnTop: false,
    clickThrough: false,
    trayReady: true,
    visible: false,
    companionSurface: "idle",
    controlPanelVisible: false,
    phase: "companion_idle",
  });
});

test("parseDesktopState rejects a missing required field", () => {
  assert.throws(
    () =>
      parseDesktopState({
        alwaysOnTop: true,
        clickThrough: false,
        trayReady: true,
        companionSurface: "idle",
        controlPanelVisible: false,
        phase: "companion_idle",
      }),
    /desktop state\.visible must be a boolean/,
  );
});

test("parseDesktopState accepts only explicit idle and chat surfaces", () => {
  const base = {
    alwaysOnTop: true,
    clickThrough: false,
    trayReady: true,
    visible: true,
    controlPanelVisible: false,
    phase: "companion_idle",
  };

  assert.equal(parseDesktopState({ ...base, companionSurface: "idle" }).companionSurface, "idle");
  assert.equal(parseDesktopState({ ...base, companionSurface: "chat" }).companionSurface, "chat");
  assert.throws(
    () => parseDesktopState({ ...base, companionSurface: "expanded" }),
    /must be idle or chat/,
  );
});

test("parseDesktopState accepts only explicit lifecycle phases", () => {
  const base = {
    alwaysOnTop: true,
    clickThrough: false,
    trayReady: true,
    visible: true,
    companionSurface: "idle",
    controlPanelVisible: false,
  };
  const phases = [
    "companion_idle",
    "companion_chat_opening",
    "companion_chat_open",
    "companion_chat_closing",
    "transitioning_to_settings",
    "control_panel_visible",
    "transitioning_to_companion",
  ];

  for (const phase of phases) {
    assert.equal(parseDesktopState({ ...base, phase }).phase, phase);
  }
  assert.throws(
    () => parseDesktopState({ ...base, phase: "switching" }),
    /explicit lifecycle phase/,
  );
});

test("parseHealthResponse accepts only the Rust architecture", () => {
  assert.deepEqual(
    parseHealthResponse({
      status: "ok",
      architecture: "tauri-rust",
      version: "0.1.0",
    }),
    {
      status: "ok",
      architecture: "tauri-rust",
      version: "0.1.0",
    },
  );

  assert.throws(
    () =>
      parseHealthResponse({
        status: "ok",
        architecture: "wails-go",
        version: "0.1.0",
      }),
    /architecture must be tauri-rust/,
  );
});

test("normalizeInvokeError preserves a structured domain failure", () => {
  assert.deepEqual(
    normalizeInvokeError({
      code: "TRAY_NOT_READY",
      message: "click-through requires an active tray recovery entry",
    }),
    {
      code: "TRAY_NOT_READY",
      message: "click-through requires an active tray recovery entry",
    },
  );
});

test("normalizeInvokeError does not expose unknown dependency text", () => {
  const error = normalizeInvokeError("token=should-not-leak");

  assert.equal(error.code, "TAURI_INVOKE_FAILED");
  assert.equal(error.message.includes("should-not-leak"), false);
});
