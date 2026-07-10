import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";

import { parseDesktopState, parseHealthResponse } from "./desktopState.mjs";

export async function getHealth() {
  return parseHealthResponse(await invoke("health"));
}

export async function getDesktopState() {
  return parseDesktopState(await invoke("get_desktop_state"));
}

export async function setAlwaysOnTop(enabled) {
  return parseDesktopState(await invoke("set_always_on_top", { enabled }));
}

export async function setClickThrough(enabled) {
  return parseDesktopState(await invoke("set_click_through", { enabled }));
}

export async function showCompanion() {
  return parseDesktopState(await invoke("show_companion"));
}

export async function hideCompanion() {
  return parseDesktopState(await invoke("hide_companion"));
}

export async function restoreCompanionInteraction() {
  return parseDesktopState(await invoke("restore_companion_interaction"));
}

export async function openCompanionChat() {
  return parseDesktopState(await invoke("open_companion_chat"));
}

export async function closeCompanionChat() {
  return parseDesktopState(await invoke("close_companion_chat"));
}

export async function showControlPanel() {
  return parseDesktopState(await invoke("show_control_panel"));
}

export async function restoreCompanionAfterControlPanel() {
  return parseDesktopState(await invoke("restore_companion_after_control_panel"));
}

export function listenToDesktopState(onState, onError) {
  return listen("desktop-state-changed", (event) => {
    try {
      onState(parseDesktopState(event.payload));
    } catch (error) {
      onError(error);
    }
  });
}
