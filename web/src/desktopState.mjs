const BOOLEAN_DESKTOP_FIELDS = Object.freeze([
  "alwaysOnTop",
  "clickThrough",
  "trayReady",
  "visible",
  "controlPanelVisible",
]);

const COMPANION_SURFACES = Object.freeze(new Set(["idle", "chat"]));
const DESKTOP_PHASES = Object.freeze(new Set([
  "companion_idle",
  "companion_chat_opening",
  "companion_chat_open",
  "companion_chat_closing",
  "transitioning_to_settings",
  "control_panel_visible",
  "transitioning_to_companion",
]));

function assertObject(value, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(`${label} must be an object`);
  }
}

export function parseDesktopState(value) {
  assertObject(value, "desktop state");
  for (const field of BOOLEAN_DESKTOP_FIELDS) {
    if (typeof value[field] !== "boolean") {
      throw new TypeError(`desktop state.${field} must be a boolean`);
    }
  }
  if (!COMPANION_SURFACES.has(value.companionSurface)) {
    throw new TypeError("desktop state.companionSurface must be idle or chat");
  }
  if (!DESKTOP_PHASES.has(value.phase)) {
    throw new TypeError("desktop state.phase must be an explicit lifecycle phase");
  }

  return Object.freeze({
    alwaysOnTop: value.alwaysOnTop,
    clickThrough: value.clickThrough,
    trayReady: value.trayReady,
    visible: value.visible,
    companionSurface: value.companionSurface,
    controlPanelVisible: value.controlPanelVisible,
    phase: value.phase,
  });
}

export function parseHealthResponse(value) {
  assertObject(value, "health response");
  if (value.status !== "ok") {
    throw new TypeError("health response.status must be ok");
  }
  if (value.architecture !== "tauri-rust" && value.architecture !== "wails-go") {
    throw new TypeError("health response.architecture must be tauri-rust or wails-go");
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
