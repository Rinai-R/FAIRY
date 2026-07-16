import { parseDesktopState, parseHealthResponse } from "./desktopState.mjs";
import { ensureWailsRuntimeReady, isWailsRuntime } from "./runtimeEnv.mjs";

async function loadWailsDesktopService() {
  try {
    const bindings = await import("../../fairy/frontend/bindings/fairy/desktop/index.js");
    return bindings.DesktopService ?? null;
  } catch {
    return null;
  }
}

async function loadTauriInvoke() {
  try {
    const mod = await import("@tauri-apps/api/core");
    return typeof mod.invoke === "function" ? mod.invoke : null;
  } catch {
    return null;
  }
}

async function callDesktopService(method, tauriCommand, wailsArgs = [], tauriArgs = undefined) {
  if (await ensureWailsRuntimeReady()) {
    const service = await loadWailsDesktopService();
    if (service !== null && typeof service[method] === "function") {
      return parseDesktopState(await service[method](...wailsArgs));
    }
    throw new Error(`DESKTOP_RUNTIME_UNAVAILABLE: Wails DesktopService.${method} is not available`);
  }
  const invoke = await loadTauriInvoke();
  if (invoke !== null) {
    return parseDesktopState(await invoke(tauriCommand, tauriArgs));
  }
  throw new Error("DESKTOP_RUNTIME_UNAVAILABLE: neither Wails DesktopService nor Tauri invoke is available");
}

export async function getHealth() {
  if (await ensureWailsRuntimeReady()) {
    const service = await loadWailsDesktopService();
    if (service === null) {
      throw new Error("HEALTH_RUNTIME_UNAVAILABLE: Wails DesktopService is not available");
    }
    await service.GetDesktopState();
    return parseHealthResponse({
      status: "ok",
      architecture: "wails-go",
      version: "0.1.0",
    });
  }
  const invoke = await loadTauriInvoke();
  if (invoke !== null) {
    return parseHealthResponse(await invoke("health"));
  }
  throw new Error("HEALTH_RUNTIME_UNAVAILABLE: neither Wails DesktopService nor Tauri invoke is available");
}

export async function getDesktopState() {
  return callDesktopService("GetDesktopState", "get_desktop_state");
}

export async function setAlwaysOnTop(enabled) {
  return callDesktopService("SetAlwaysOnTop", "set_always_on_top", [enabled], { enabled });
}

export async function setClickThrough(enabled) {
  return callDesktopService("SetClickThrough", "set_click_through", [enabled], { enabled });
}

export async function showCompanion() {
  return callDesktopService("ShowCompanion", "show_companion");
}

export async function hideCompanion() {
  return callDesktopService("HideCompanion", "hide_companion");
}

export async function restoreCompanionInteraction() {
  return callDesktopService("RestoreCompanionInteraction", "restore_companion_interaction");
}

export async function openCompanionChat() {
  return callDesktopService("OpenCompanionChat", "open_companion_chat");
}

export async function closeCompanionChat() {
  return callDesktopService("CloseCompanionChat", "close_companion_chat");
}

export async function showControlPanel() {
  return callDesktopService("ShowControlPanel", "show_control_panel");
}

export async function restoreCompanionAfterControlPanel() {
  return callDesktopService("RestoreCompanionAfterControlPanel", "restore_companion_after_control_panel");
}

export async function listenToDesktopState(onState, onError) {
  if (await ensureWailsRuntimeReady()) {
    const { Events } = await import("@wailsio/runtime");
    if (typeof Events?.On !== "function") {
      throw new Error("Wails Events.On is unavailable");
    }
    const off = Events.On("desktop-state-changed", (event) => {
      try {
        onState(parseDesktopState(event.data ?? event));
      } catch (error) {
        onError(error);
      }
    });
    return typeof off === "function" ? off : () => {};
  }
  const { listen } = await import("@tauri-apps/api/event");
  return listen("desktop-state-changed", (event) => {
    try {
      onState(parseDesktopState(event.payload));
    } catch (error) {
      onError(error);
    }
  });
}
