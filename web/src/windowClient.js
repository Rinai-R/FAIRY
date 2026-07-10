import { listen } from "@tauri-apps/api/event";
import { getCurrentWindow } from "@tauri-apps/api/window";

import {
  parseConfigurationChange,
  parseProductWindowLabel,
} from "./windowState.mjs";

export function currentProductWindowLabel() {
  return parseProductWindowLabel(getCurrentWindow().label);
}

export function listenToConfigurationChanges(onChange, onError) {
  return listen("companion-configuration-changed", (event) => {
    try {
      onChange(parseConfigurationChange(event.payload));
    } catch (error) {
      onError(error);
    }
  });
}
