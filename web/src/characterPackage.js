export const CHARACTER_PACKAGE_TYPE = "fairy.character-pack";
export const CHARACTER_PACKAGE_VERSION = 1;

const SENSITIVE_KEY_PATTERN = /(api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|password|credential|authorization)/i;

export function createCharacterPackage(characters, options = {}) {
  const list = normalizeCharacterList(characters);
  if (!list.length) {
    throw new Error("角色包至少需要包含一个角色");
  }
  const redactSensitive = options.redactSensitive !== false;
  return {
    type: CHARACTER_PACKAGE_TYPE,
    version: CHARACTER_PACKAGE_VERSION,
    exported_at: options.exportedAt || new Date().toISOString(),
    characters: list.map((character) => redactSensitive ? redactSensitiveFields(character) : clonePackageValue(character)),
  };
}

export function parseCharacterPackage(input) {
  const payload = typeof input === "string" ? parsePackageJSON(input) : input;
  if (!payload || typeof payload !== "object") {
    throw new Error("角色包格式无效");
  }
  if (payload.type && payload.type !== CHARACTER_PACKAGE_TYPE) {
    throw new Error("不是 FAIRY 角色包");
  }
  const characters = normalizeCharacterList(payload.characters);
  if (!characters.length) {
    throw new Error("角色包缺少 characters 数组");
  }
  return {
    type: CHARACTER_PACKAGE_TYPE,
    version: Number(payload.version) || CHARACTER_PACKAGE_VERSION,
    exported_at: payload.exported_at || "",
    characters,
  };
}

export function mergeCharacterPackage(existingCharacters, packageInput) {
  const pack = parseCharacterPackage(packageInput);
  const current = normalizeCharacterList(existingCharacters);
  const byID = new Map(current.map((character) => [character.id, character]));
  const order = current.map((character) => character.id);
  for (const character of pack.characters) {
    if (!byID.has(character.id)) {
      order.push(character.id);
    }
    byID.set(character.id, character);
  }
  return {
    characters: order.map((id) => byID.get(id)).filter(Boolean),
    firstCharacterID: pack.characters[0]?.id || "",
    importedCount: pack.characters.length,
  };
}

export function redactSensitiveFields(value) {
  if (Array.isArray(value)) {
    return value.map((item) => redactSensitiveFields(item));
  }
  if (!value || typeof value !== "object") {
    return value;
  }
  const out = {};
  for (const [key, item] of Object.entries(value)) {
    out[key] = SENSITIVE_KEY_PATTERN.test(key) ? "" : redactSensitiveFields(item);
  }
  return out;
}

function clonePackageValue(value) {
  if (Array.isArray(value)) {
    return value.map((item) => clonePackageValue(item));
  }
  if (!value || typeof value !== "object") {
    return value;
  }
  const out = {};
  for (const [key, item] of Object.entries(value)) {
    out[key] = clonePackageValue(item);
  }
  return out;
}

function parsePackageJSON(input) {
  try {
    return JSON.parse(input);
  } catch {
    throw new Error("角色包 JSON 无法解析");
  }
}

function normalizeCharacterList(characters) {
  if (!Array.isArray(characters)) return [];
  return characters
    .map((character) => normalizeCharacter(character))
    .filter(Boolean);
}

function normalizeCharacter(character) {
  if (!character || typeof character !== "object") return null;
  const id = String(character.id || "").trim();
  if (!id) return null;
  return {
    ...character,
    id,
    display_name: String(character.display_name || character.name || id).trim(),
    assets: character.assets && typeof character.assets === "object" ? character.assets : {},
    runtime: character.runtime && typeof character.runtime === "object" ? character.runtime : {},
    prompt: character.prompt && typeof character.prompt === "object" ? character.prompt : undefined,
    style_rules: Array.isArray(character.style_rules) ? character.style_rules : [],
  };
}
