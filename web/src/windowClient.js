import {
  parseConfigurationChange,
  parseProductWindowLabel,
} from "./windowState.mjs";
import { isWailsRuntime } from "./runtimeEnv.mjs";

export { isWailsRuntime };

function wailsSurfaceFromLocation() {
  if (typeof window === "undefined" || typeof window.location?.search !== "string") {
    return "companion";
  }
  const surface = new URLSearchParams(window.location.search).get("surface");
  if (surface === "control-panel") return "control-panel";
  return "companion";
}

export function currentProductWindowLabel() {
  return parseProductWindowLabel(wailsSurfaceFromLocation());
}

export async function startCurrentWindowDrag() {
  // Wails v3 starts native window drag from CSS `--wails-draggable: drag`
  // after mousedown + mousemove (runtime invoke "wails:drag"). There is no
  // separate Window.startDragging() bridge; the companion surface must not
  // preventDefault on pointerdown or that mousedown never arrives.
}

export async function listenToConfigurationChanges(onChange, onError) {
  if (!isWailsRuntime()) {
    return () => {};
  }
  const { Events } = await import("@wailsio/runtime");
  if (typeof Events?.On !== "function") {
    throw new Error("wails events unavailable");
  }
  const off = Events.On("companion-configuration-changed", (event) => {
    try {
      onChange(parseConfigurationChange(event.data ?? event));
    } catch (error) {
      onError(error);
    }
  });
  return typeof off === "function" ? off : () => {};
}
