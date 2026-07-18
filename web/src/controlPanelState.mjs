import {
  resolveCharacterImageUrl,
  selectVisualStateImage,
} from "./pixelTexture.mjs";

export const CONTROL_PANEL_SECTIONS = Object.freeze([
  Object.freeze({ id: "character", label: "角色" }),
  Object.freeze({ id: "profile", label: "称呼" }),
  Object.freeze({ id: "model", label: "模型" }),
  Object.freeze({ id: "speech", label: "语音" }),
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
export const DEFAULT_SPEECH_BASE_URL = "https://openspeech.bytedance.com";
export const DEFAULT_SPEECH_TRAIN_PATH = "/api/v3/tts/voice_clone";
export const DEFAULT_SPEECH_QUERY_PATH = "/api/v3/tts/get_voice";
export const DEFAULT_SPEECH_UPGRADE_PATH = "/upgrade_voice";
export const DEFAULT_SPEECH_SYNTHESIS_RESOURCE_ID = "seed-icl-1.0";
export const DEFAULT_SPEECH_SYNTHESIS_MODEL = "";
export const DEFAULT_SPEECH_AUDIO_FORMAT = "wav";
export const DEFAULT_SPEECH_LANGUAGE = 0;
export const DEFAULT_CHARACTER_SPEAKING_LANGUAGE = "ja";
export const DEFAULT_CHARACTER_TEXT_LANGUAGE = "zh";
export const CHARACTER_SPEAKING_LANGUAGE_OPTIONS = Object.freeze([
  Object.freeze({ value: "ja", label: "日语" }),
  Object.freeze({ value: "zh", label: "中文" }),
  Object.freeze({ value: "en", label: "英文" }),
]);
export const CHARACTER_TEXT_LANGUAGE_OPTIONS = CHARACTER_SPEAKING_LANGUAGE_OPTIONS;
const CHARACTER_SPEAKING_LANGUAGE_VALUES = new Set(CHARACTER_SPEAKING_LANGUAGE_OPTIONS.map(({ value }) => value));
const CHARACTER_TEXT_LANGUAGE_VALUES = CHARACTER_SPEAKING_LANGUAGE_VALUES;
export const SPEECH_LANGUAGE_OPTIONS = Object.freeze([
  Object.freeze({ value: 0, code: "cn", label: "中文" }),
  Object.freeze({ value: 1, code: "en", label: "英文" }),
  Object.freeze({ value: 2, code: "ja", label: "日语" }),
  Object.freeze({ value: 3, code: "es", label: "西班牙语" }),
  Object.freeze({ value: 4, code: "id", label: "印尼语" }),
  Object.freeze({ value: 5, code: "pt", label: "葡萄牙语" }),
  Object.freeze({ value: 6, code: "de", label: "德语" }),
  Object.freeze({ value: 7, code: "fr", label: "法语" }),
  Object.freeze({ value: 8, code: "ko", label: "韩语" }),
  Object.freeze({ value: 9, code: "it", label: "意大利语" }),
  Object.freeze({ value: 10, code: "th", label: "泰语" }),
  Object.freeze({ value: 11, code: "vi", label: "越南语" }),
  Object.freeze({ value: 12, code: "ru", label: "俄语" }),
  Object.freeze({ value: 13, code: "fil", label: "菲律宾语" }),
  Object.freeze({ value: 14, code: "ms", label: "马来语" }),
  Object.freeze({ value: 15, code: "ar", label: "阿拉伯语" }),
  Object.freeze({ value: 16, code: "mx", label: "墨西哥西班牙语" }),
  Object.freeze({ value: 17, code: "pt-br", label: "巴西葡萄牙语" }),
  Object.freeze({ value: 19, code: "pl", label: "波兰语" }),
  Object.freeze({ value: 20, code: "tr", label: "土耳其语" }),
  Object.freeze({ value: 21, code: "sv", label: "瑞典语" }),
]);
export const SPEECH_LANGUAGE_CODE_TO_VALUE = Object.freeze(Object.fromEntries(
  SPEECH_LANGUAGE_OPTIONS.map(({ code, value }) => [code, value]),
));
const SPEECH_LANGUAGE_VALUES = new Set(SPEECH_LANGUAGE_OPTIONS.map(({ value }) => value));

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

export function buildSpeechSettingsInput({
  enabled,
  baseUrl,
  trainPath,
  queryPath,
  upgradePath,
  appId,
  synthesisResourceId = DEFAULT_SPEECH_SYNTHESIS_RESOURCE_ID,
  synthesisModel = DEFAULT_SPEECH_SYNTHESIS_MODEL,
  apiKey = "",
  accessToken = "",
  clearApiKey = false,
  clearAccessToken = false,
  defaultSpeaker,
  defaultLanguage,
  defaultFormat,
}) {
  const normalizedBaseUrl = String(baseUrl ?? "").trim();
  const normalizedTrainPath = normalizeSpeechPath(trainPath, DEFAULT_SPEECH_TRAIN_PATH);
  const normalizedQueryPath = normalizeSpeechPath(queryPath, DEFAULT_SPEECH_QUERY_PATH);
  const normalizedUpgradePath = normalizeSpeechPath(upgradePath, DEFAULT_SPEECH_UPGRADE_PATH);
  const normalizedAppId = String(appId ?? "").trim();
  const normalizedSynthesisResourceId = String(synthesisResourceId ?? "").trim() || DEFAULT_SPEECH_SYNTHESIS_RESOURCE_ID;
  const normalizedSynthesisModel = String(synthesisModel ?? "").trim() || DEFAULT_SPEECH_SYNTHESIS_MODEL;
  const normalizedDefaultSpeaker = String(defaultSpeaker ?? "").trim();
  const normalizedDefaultFormat = normalizeSpeechFormat(defaultFormat || DEFAULT_SPEECH_AUDIO_FORMAT);
  const numericDefaultLanguage = parseSpeechLanguage(defaultLanguage, "speech default language");
  let parsedBaseUrl;
  try {
    parsedBaseUrl = new URL(normalizedBaseUrl);
  } catch {
    throw new TypeError("speech base URL must be a valid http/https URL");
  }
  if (parsedBaseUrl.protocol !== "https:" && parsedBaseUrl.protocol !== "http:") {
    throw new TypeError("speech base URL must use http or https");
  }
  if (normalizedDefaultFormat.length === 0) {
    throw new TypeError("speech default audio format is required");
  }
  return Object.freeze({
    enabled: Boolean(enabled),
    baseUrl: normalizedBaseUrl,
    trainPath: normalizedTrainPath,
    queryPath: normalizedQueryPath,
    upgradePath: normalizedUpgradePath,
    appId: normalizedAppId,
    synthesisResourceId: normalizedSynthesisResourceId,
    synthesisModel: normalizedSynthesisModel,
    apiKey: String(apiKey ?? ""),
    accessToken: String(accessToken ?? ""),
    clearApiKey: Boolean(clearApiKey),
    clearAccessToken: Boolean(clearAccessToken),
    defaultSpeaker: normalizedDefaultSpeaker,
    defaultLanguage: numericDefaultLanguage,
    defaultFormat: normalizedDefaultFormat,
  });
}

export function buildSpeechTrainInput({ speakerId, audioData, audioFormat, language }) {
  const normalizedSpeaker = String(speakerId ?? "").trim();
  const normalizedAudioData = String(audioData ?? "").trim();
  const normalizedAudioFormat = normalizeSpeechFormat(audioFormat || DEFAULT_SPEECH_AUDIO_FORMAT);
  const numericLanguage = parseSpeechLanguage(language, "speech language");
  if (normalizedSpeaker.length === 0) {
    throw new TypeError("speech speaker id is required");
  }
  if (normalizedAudioData.length === 0) {
    throw new TypeError("speech audio data is required");
  }
  if (normalizedAudioFormat.length === 0) {
    throw new TypeError("speech audio format is required");
  }
  return Object.freeze({
    speakerId: normalizedSpeaker,
    audioData: normalizedAudioData,
    audioFormat: normalizedAudioFormat,
    language: numericLanguage,
  });
}

export function buildSpeechSpeakerInput({ speakerId }) {
  const normalizedSpeaker = String(speakerId ?? "").trim();
  if (normalizedSpeaker.length === 0) {
    throw new TypeError("speech speaker id is required");
  }
  return Object.freeze({ speakerId: normalizedSpeaker });
}

function normalizeSpeechPath(value, fallback) {
  const normalized = String(value ?? "").trim() || fallback;
  return normalized.startsWith("/") ? normalized : `/${normalized}`;
}

function normalizeSpeechFormat(value) {
  return String(value ?? "").trim().replace(/^\./, "").toLowerCase();
}

function parseSpeechLanguage(value, fieldName) {
  const numericLanguage = Number(String(value ?? DEFAULT_SPEECH_LANGUAGE).trim());
  if (!Number.isSafeInteger(numericLanguage) || numericLanguage < 0) {
    throw new TypeError(`${fieldName} must be a non-negative integer`);
  }
  if (!SPEECH_LANGUAGE_VALUES.has(numericLanguage)) {
    throw new TypeError(`unsupported ${fieldName}`);
  }
  return numericLanguage;
}

const VISUAL_PACK_ID_PATTERN = /^[a-z0-9](?:[a-z0-9.-]{0,62}[a-z0-9])?$/;

export function normalizeCharacterSpeakingLanguage(value) {
  const normalized = String(value ?? "").trim() || DEFAULT_CHARACTER_SPEAKING_LANGUAGE;
  if (!CHARACTER_SPEAKING_LANGUAGE_VALUES.has(normalized)) {
    throw new TypeError("character speaking language is unsupported");
  }
  return normalized;
}

export function normalizeCharacterTextLanguage(value) {
  const normalized = String(value ?? "").trim() || DEFAULT_CHARACTER_TEXT_LANGUAGE;
  if (!CHARACTER_TEXT_LANGUAGE_VALUES.has(normalized)) {
    throw new TypeError("character text language is unsupported");
  }
  return normalized;
}

export function buildCharacterSaveInput({
  name,
  description,
  dialogueStyle = "",
  textLanguage = DEFAULT_CHARACTER_TEXT_LANGUAGE,
  speakingLanguage = DEFAULT_CHARACTER_SPEAKING_LANGUAGE,
  visualPackId,
}) {
  if (typeof name !== "string" || typeof description !== "string") {
    throw new TypeError("character name and description are required");
  }
  if (typeof dialogueStyle !== "string") {
    throw new TypeError("character dialogue style must be a string");
  }
  const normalizedName = name.trim();
  const normalizedDescription = description.trim();
  const normalizedDialogueStyle = dialogueStyle.trim();
  const normalizedTextLanguage = normalizeCharacterTextLanguage(textLanguage);
  const normalizedSpeakingLanguage = normalizeCharacterSpeakingLanguage(speakingLanguage);
  if (normalizedName.length === 0 || normalizedDescription.length === 0) {
    throw new TypeError("character name and description are required");
  }
  if (typeof visualPackId !== "string" || !VISUAL_PACK_ID_PATTERN.test(visualPackId)) {
    throw new TypeError("character visual pack must be selected");
  }
  const brief = {
    name: normalizedName,
    description: normalizedDescription,
    textLanguage: normalizedTextLanguage,
    speakingLanguage: normalizedSpeakingLanguage,
  };
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
