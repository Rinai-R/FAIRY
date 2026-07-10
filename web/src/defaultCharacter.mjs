export const DEFAULT_CHARACTER = Object.freeze({
  id: "atri.official-prototype",
  displayName: "ATRI",
  renderer: "static-png",
  assetPath: "/characters/atri/atri-official.png",
});

export function isPackagedAssetPath(path) {
  return (
    typeof path === "string" &&
    path.startsWith("/") &&
    !path.startsWith("//") &&
    !path.includes("://")
  );
}

export function validateCharacter(character) {
  if (character === null || typeof character !== "object") {
    throw new TypeError("character must be an object");
  }
  if (typeof character.id !== "string" || character.id.length === 0) {
    throw new TypeError("character.id must be a non-empty string");
  }
  if (character.renderer !== "static-png") {
    throw new TypeError("character.renderer must be static-png");
  }
  if (!isPackagedAssetPath(character.assetPath)) {
    throw new TypeError("character.assetPath must be a packaged local path");
  }

  return character;
}

export function describeCharacterFailure(character) {
  validateCharacter(character);
  return Object.freeze({
    code: "CHARACTER_ASSET_FAILED",
    message: `无法加载 ${character.displayName} 的内置角色资源。`,
  });
}

validateCharacter(DEFAULT_CHARACTER);
