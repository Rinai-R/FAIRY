export const CONTROL_PANEL_SECTIONS = Object.freeze([
  Object.freeze({ id: "character", label: "角色" }),
  Object.freeze({ id: "profile", label: "称呼" }),
  Object.freeze({ id: "model", label: "模型" }),
  Object.freeze({ id: "intelligence", label: "智能" }),
  Object.freeze({ id: "desktop", label: "桌面" }),
]);

export const MODEL_PROTOCOL_OPTIONS = Object.freeze([
  Object.freeze({ value: "chat_completions", label: "Chat Completions" }),
  Object.freeze({ value: "responses", label: "Responses" }),
]);
export const DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS = 128_000;
export const MIN_MODEL_CONTEXT_WINDOW_TOKENS = 4_096;
export const MAX_MODEL_CONTEXT_WINDOW_TOKENS = 2_000_000;

export function assertControlPanelSection(value) {
  if (!CONTROL_PANEL_SECTIONS.some((section) => section.id === value)) {
    throw new TypeError("unsupported control panel section");
  }
  return value;
}

function parseContextWindowTokens(value) {
  const numericValue = typeof value === "string" ? Number(value.trim()) : value;
  if (
    !Number.isSafeInteger(numericValue) ||
    numericValue < MIN_MODEL_CONTEXT_WINDOW_TOKENS ||
    numericValue > MAX_MODEL_CONTEXT_WINDOW_TOKENS
  ) {
    throw new TypeError("model context window must be 4096-2000000 tokens");
  }
  return numericValue;
}

export function buildModelConnectionInput({ protocol, endpoint, model, contextWindowTokens, authMode }) {
  if (!MODEL_PROTOCOL_OPTIONS.some((option) => option.value === protocol)) {
    throw new TypeError("unsupported model protocol");
  }
  if (authMode !== "bearer_key" && authMode !== "no_auth") {
    throw new TypeError("unsupported model auth mode");
  }
  const normalizedEndpoint = endpoint.trim();
  const normalizedModel = model.trim();
  if (normalizedEndpoint.length === 0 || normalizedModel.length === 0) {
    throw new TypeError("model base URL and model are required");
  }
  return Object.freeze({
    protocol,
    endpoint: normalizedEndpoint,
    model: normalizedModel,
    contextWindowTokens: parseContextWindowTokens(contextWindowTokens),
    authMode,
  });
}

const VISUAL_PACK_ID_PATTERN = /^[a-z0-9](?:[a-z0-9.-]{0,62}[a-z0-9])?$/;

export function buildCharacterSaveInput({ name, description, dialogueStyle = "", visualPackId }) {
  if (typeof name !== "string" || typeof description !== "string") {
    throw new TypeError("character name and description are required");
  }
  if (typeof dialogueStyle !== "string") {
    throw new TypeError("character dialogue style must be a string");
  }
  const normalizedName = name.trim();
  const normalizedDescription = description.trim();
  const normalizedDialogueStyle = dialogueStyle.trim();
  if (normalizedName.length === 0 || normalizedDescription.length === 0) {
    throw new TypeError("character name and description are required");
  }
  if (typeof visualPackId !== "string" || !VISUAL_PACK_ID_PATTERN.test(visualPackId)) {
    throw new TypeError("character visual pack must be selected");
  }
  const brief = { name: normalizedName, description: normalizedDescription };
  if (normalizedDialogueStyle.length > 0) {
    brief.dialogueStyle = normalizedDialogueStyle;
  }
  return Object.freeze({
    brief: Object.freeze(brief),
    visualPackId,
  });
}

export function selectedAppearancePackId(character) {
  if (character === null) return "";
  if (character.appearance.status !== "assigned") return "";
  return character.appearance.visual.packId;
}
