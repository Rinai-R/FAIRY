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
  });

  assert.deepEqual(state, {
    alwaysOnTop: false,
    clickThrough: false,
    trayReady: true,
    visible: false,
  });
});

test("parseDesktopState rejects a missing required field", () => {
  assert.throws(
    () =>
      parseDesktopState({
        alwaysOnTop: true,
        clickThrough: false,
        trayReady: true,
      }),
    /desktop state\.visible must be a boolean/,
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

