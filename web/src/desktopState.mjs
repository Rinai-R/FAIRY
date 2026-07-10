const DESKTOP_FIELDS = Object.freeze([
  "alwaysOnTop",
  "clickThrough",
  "trayReady",
  "visible",
]);

function assertObject(value, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(`${label} must be an object`);
  }
}

export function parseDesktopState(value) {
  assertObject(value, "desktop state");
  for (const field of DESKTOP_FIELDS) {
    if (typeof value[field] !== "boolean") {
      throw new TypeError(`desktop state.${field} must be a boolean`);
    }
  }

  return Object.freeze({
    alwaysOnTop: value.alwaysOnTop,
    clickThrough: value.clickThrough,
    trayReady: value.trayReady,
    visible: value.visible,
  });
}

export function parseHealthResponse(value) {
  assertObject(value, "health response");
  if (value.status !== "ok") {
    throw new TypeError("health response.status must be ok");
  }
  if (value.architecture !== "tauri-rust") {
    throw new TypeError("health response.architecture must be tauri-rust");
  }
  if (typeof value.version !== "string" || value.version.length === 0) {
    throw new TypeError("health response.version must be a non-empty string");
  }

  return Object.freeze({
    status: value.status,
    architecture: value.architecture,
    version: value.version,
  });
}

export function normalizeInvokeError(error) {
  if (
    error !== null &&
    typeof error === "object" &&
    !Array.isArray(error) &&
    typeof error.code === "string" &&
    error.code.length > 0 &&
    typeof error.message === "string" &&
    error.message.length > 0
  ) {
    return Object.freeze({ code: error.code, message: error.message });
  }

  return Object.freeze({
    code: "TAURI_INVOKE_FAILED",
    message: "FAIRY 无法完成桌面请求，请从菜单栏退出后重试。",
  });
}

