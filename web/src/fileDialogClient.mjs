import { isWailsRuntime } from "./runtimeEnv.mjs";

function sanitizePackageBaseName(characterName = "character") {
  return (
    String(characterName)
      .trim()
      .replace(/[\\/:*?"<>|\u0000-\u001f]/g, "-")
      .replace(/^\.+$/, "") || "character"
  );
}

async function selectWithWailsOpen() {
  const { Dialogs } = await import("@wailsio/runtime");
  const selected = await Dialogs.OpenFile({
    Title: "导入角色包",
    CanChooseFiles: true,
    CanChooseDirectories: false,
    AllowsMultipleSelection: false,
    Filters: [{ DisplayName: "FAIRY 角色包", Pattern: "*.pack;*.zip" }],
  });
  if (typeof selected !== "string" || selected.trim().length === 0) return null;
  return selected;
}

async function selectWithWailsSave(characterName) {
  const { Dialogs } = await import("@wailsio/runtime");
  const selected = await Dialogs.SaveFile({
    Title: "导出角色包",
    Filename: `${sanitizePackageBaseName(characterName)}.pack`,
    CanCreateDirectories: true,
    Filters: [{ DisplayName: "FAIRY 角色包", Pattern: "*.pack" }],
  });
  if (typeof selected !== "string" || selected.trim().length === 0) return null;
  return selected;
}

async function selectWithTauriOpen() {
  const { open: openDialog } = await import("@tauri-apps/plugin-dialog");
  const selected = await openDialog({
    title: "导入角色包",
    multiple: false,
    filters: [{ name: "FAIRY 角色包", extensions: ["pack", "zip"] }],
  });
  if (Array.isArray(selected)) return selected[0] ?? null;
  return typeof selected === "string" ? selected : null;
}

async function selectWithTauriSave(characterName) {
  const { save: saveDialog } = await import("@tauri-apps/plugin-dialog");
  const selected = await saveDialog({
    title: "导出角色包",
    defaultPath: `${sanitizePackageBaseName(characterName)}.pack`,
    filters: [{ name: "FAIRY 角色包", extensions: ["pack"] }],
  });
  return typeof selected === "string" ? selected : null;
}

/** Prefer Wails Dialogs; keep Tauri plugin-dialog as fallback outside Wails. */
export async function selectCharacterPackageFile() {
  if (isWailsRuntime()) {
    return selectWithWailsOpen();
  }
  return selectWithTauriOpen();
}

/** Prefer Wails Dialogs; keep Tauri plugin-dialog as fallback outside Wails. */
export async function selectCharacterPackageSavePath(characterName = "character") {
  if (isWailsRuntime()) {
    return selectWithWailsSave(characterName);
  }
  return selectWithTauriSave(characterName);
}
