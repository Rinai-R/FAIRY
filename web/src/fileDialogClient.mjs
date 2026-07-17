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

export async function selectCharacterPackageFile() {
  if (!isWailsRuntime()) {
    throw new Error("DESKTOP_RUNTIME_UNAVAILABLE: Wails runtime is required for file dialogs");
  }
  return selectWithWailsOpen();
}

export async function selectCharacterPackageSavePath(characterName = "character") {
  if (!isWailsRuntime()) {
    throw new Error("DESKTOP_RUNTIME_UNAVAILABLE: Wails runtime is required for file dialogs");
  }
  return selectWithWailsSave(characterName);
}
