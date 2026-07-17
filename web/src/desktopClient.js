import { parseDesktopState, parseHealthResponse } from "./desktopState.mjs";
import { ensureWailsRuntimeReady } from "./runtimeEnv.mjs";

async function loadWailsDesktopService() {
  try {
    const bindings = await import("../../fairy/frontend/bindings/fairy/desktop/index.js");
    return bindings.DesktopService ?? null;
  } catch {
    return null;
  }
}

async function callDesktopService(method, wailsArgs = []) {
  if (!(await ensureWailsRuntimeReady())) {
    throw new Error("DESKTOP_RUNTIME_UNAVAILABLE: Wails runtime is required");
  }
  const service = await loadWailsDesktopService();
  if (service === null || typeof service[method] !== "function") {
    throw new Error(`DESKTOP_RUNTIME_UNAVAILABLE: Wails DesktopService.${method} is not available`);
  }
  return parseDesktopState(await service[method](...wailsArgs));
}

export async function getHealth() {
  if (!(await ensureWailsRuntimeReady())) {
    throw new Error("HEALTH_RUNTIME_UNAVAILABLE: Wails runtime is required");
  }
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

export async function getDesktopState() {
  return callDesktopService("GetDesktopState");
}

export async function setAlwaysOnTop(enabled) {
  return callDesktopService("SetAlwaysOnTop", [enabled]);
}

export async function setClickThrough(enabled) {
  return callDesktopService("SetClickThrough", [enabled]);
}

export async function showCompanion() {
  return callDesktopService("ShowCompanion");
}

export async function hideCompanion() {
  return callDesktopService("HideCompanion");
}

export async function restoreCompanionInteraction() {
  return callDesktopService("RestoreCompanionInteraction");
}

export async function openCompanionChat() {
  return callDesktopService("OpenCompanionChat");
}

export async function closeCompanionChat() {
  return callDesktopService("CloseCompanionChat");
}

export async function showControlPanel() {
  return callDesktopService("ShowControlPanel");
}

export async function restoreCompanionAfterControlPanel() {
  return callDesktopService("RestoreCompanionAfterControlPanel");
}

export async function listenToDesktopState(onState, onError) {
  if (!(await ensureWailsRuntimeReady())) {
    return () => {};
  }
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
