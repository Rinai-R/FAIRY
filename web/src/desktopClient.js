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

export function listenToDesktopState(onState, onError) {
  return listen("desktop-state-changed", (event) => {
    try {
      onState(parseDesktopState(event.payload));
    } catch (error) {
      onError(error);
    }
  });
}
