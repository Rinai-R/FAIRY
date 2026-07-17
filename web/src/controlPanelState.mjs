import {
  resolveCharacterImageUrl,
  selectVisualStateImage,
} from "./pixelTexture.mjs";

export const CONTROL_PANEL_SECTIONS = Object.freeze([
  Object.freeze({ id: "character", label: "角色" }),
  Object.freeze({ id: "profile", label: "称呼" }),
  Object.freeze({ id: "model", label: "模型" }),
  Object.freeze({ id: "intelligence", label: "智能" }),
  Object.freeze({ id: "usage", label: "用量" }),
  Object.freeze({ id: "desktop", label: "桌面" }),
]);

export const MODEL_PROTOCOL_OPTIONS = Object.freeze([
  Object.freeze({ value: "chat_completions", label: "Chat Completions" }),
  Object.freeze({ value: "responses", label: "Responses" }),
]);
export const DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS = 128_000;
export const MIN_MODEL_CONTEXT_WINDOW_TOKENS = 4_096;
export const MAX_MODEL_CONTEXT_WINDOW_TOKENS = 2_000_000;
export const DEEPSEEK_V4_CONTEXT_WINDOW_TOKENS = 1_048_576;

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

function endpointHost(value) {
  try {
    return new URL(value.trim()).hostname.toLowerCase();
  } catch {
    return "";
  }
}

export function suggestModelContextWindowTokens({ endpoint, model }) {
  const host = endpointHost(endpoint);
  const normalizedModel = model.trim().toLowerCase();
  if (
    (host === "api.deepseek.com" || host.endsWith(".deepseek.com")) &&
    (normalizedModel === "deepseek-v4-flash" || normalizedModel.startsWith("deepseek-v4-"))
  ) {
    return DEEPSEEK_V4_CONTEXT_WINDOW_TOKENS;
  }
  return DEFAULT_MODEL_CONTEXT_WINDOW_TOKENS;
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

/** Resolve a visual pack idle image for control-panel <img> tags (Wails/Tauri). */
export function controlPanelVisualPreviewUrl(visual, origin) {
  if (visual === null || visual === undefined) return "";
  if (typeof origin !== "string" || origin.length === 0) return "";
  try {
    const idle = selectVisualStateImage(visual, "idle");
    return resolveCharacterImageUrl(idle.imagePath, origin);
  } catch {
    return "";
  }
}

/** Resolve an assigned character's idle preview, or "" when unassigned/unavailable. */
export function controlPanelCharacterPreviewUrl(character, origin) {
  if (character === null || character === undefined) return "";
  if (character.appearance?.status !== "assigned") return "";
  return controlPanelVisualPreviewUrl(character.appearance.visual, origin);
}
