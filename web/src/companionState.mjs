const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const TURN_STATES = new Set([
  "idle",
  "interpreting",
  "gathering",
  "planning",
  "responding",
  "completed",
  "interrupted",
  "failed",
]);
const LANES = new Set(["respond", "compact", "extract"]);
const CHARACTER_SPEAKING_LANGUAGES = new Set(["ja", "zh", "en"]);
const AUDIO_DATA_URL_PATTERN = /^data:audio\/[a-z0-9.+-]+;base64,[a-z0-9+/=]+$/i;
const MIN_MODEL_CONTEXT_WINDOW_TOKENS = 4_096;
const MAX_MODEL_CONTEXT_WINDOW_TOKENS = 2_000_000;

function assertObject(value, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(`${label} must be an object`);
  }
}

function assertExactKeys(value, keys, label) {
  assertObject(value, label);
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (
    actual.length !== expected.length ||
    actual.some((key, index) => key !== expected[index])
  ) {
    throw new TypeError(`${label} has an invalid field set`);
  }
}

function parseUuid(value, label) {
  if (typeof value !== "string" || !UUID_PATTERN.test(value)) {
    throw new TypeError(`${label} must be a UUID`);
  }
  return value;
}

function parseRevision(value, label) {
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new TypeError(`${label} must be a positive integer`);
  }
  return value;
}

function parseOptionalRevision(value, label) {
  return value === null ? null : parseRevision(value, label);
}

function parseTurnState(value, label = "turn state") {
  if (!TURN_STATES.has(value)) {
    throw new TypeError(`${label} is unsupported`);
  }
  return value;
}

function parseNonEmptyString(value, label) {
  if (typeof value !== "string" || value.length === 0) {
    throw new TypeError(`${label} must be a non-empty string`);
  }
  return value;
}

function parseString(value, label) {
  if (typeof value !== "string") {
    throw new TypeError(`${label} must be a string`);
  }
  return value;
}

function parseOptionalNonEmptyString(value, label) {
  return value === null ? null : parseNonEmptyString(value, label);
}

function parseCharacterSpeakingLanguage(value, label) {
  if (!CHARACTER_SPEAKING_LANGUAGES.has(value)) {
    throw new TypeError(`${label} is unsupported`);
  }
  return value;
}

function parseOptionalTokenCount(value, label) {
  if (value === null) return null;
  if (!Number.isSafeInteger(value) || value < 0) {
    throw new TypeError(`${label} must be null or a non-negative integer`);
  }
  return value;
}

function parseNonNegativeInteger(value, label) {
  if (!Number.isSafeInteger(value) || value < 0) {
    throw new TypeError(`${label} must be a non-negative integer`);
  }
  return value;
}

function parseContextWindowTokens(value, label) {
  if (
    !Number.isSafeInteger(value) ||
    value < MIN_MODEL_CONTEXT_WINDOW_TOKENS ||
    value > MAX_MODEL_CONTEXT_WINDOW_TOKENS
  ) {
    throw new TypeError(`${label} must be between 4096 and 2000000`);
  }
  return value;
}

function parseCacheObservation(value, label) {
  assertObject(value, label);
  if (value.status === "observed") {
    assertExactKeys(value, ["status", "tokens"], label);
    const tokens = parseOptionalTokenCount(value.tokens, `${label}.tokens`);
    if (tokens === null) {
      throw new TypeError(`${label}.tokens must be a non-negative integer`);
    }
    return Object.freeze({
      status: "observed",
      tokens,
    });
  }
  if (value.status === "unsupported" || value.status === "missing") {
    assertExactKeys(value, ["status"], label);
    return Object.freeze({ status: value.status });
  }
  throw new TypeError(`${label}.status is unsupported`);
}

function parseLaneUsage(value, index) {
  const label = `usage[${index}]`;
  assertExactKeys(value, ["lane", "historyWindow", "usage"], label);
  if (!LANES.has(value.lane)) {
    throw new TypeError(`${label}.lane is unsupported`);
  }
  assertExactKeys(
    value.usage,
    [
      "inputTokens",
      "outputTokens",
      "cachedInputTokens",
      "cacheWriteTokens",
    ],
    `${label}.usage`,
  );
  return Object.freeze({
    lane: value.lane,
    historyWindow: parseRevision(
      value.historyWindow,
      `${label}.historyWindow`,
    ),
    usage: Object.freeze({
      inputTokens: parseOptionalTokenCount(
        value.usage.inputTokens,
        `${label}.usage.inputTokens`,
      ),
      outputTokens: parseOptionalTokenCount(
        value.usage.outputTokens,
        `${label}.usage.outputTokens`,
      ),
      cachedInputTokens: parseCacheObservation(
        value.usage.cachedInputTokens,
        `${label}.usage.cachedInputTokens`,
      ),
      cacheWriteTokens: parseCacheObservation(
        value.usage.cacheWriteTokens,
        `${label}.usage.cacheWriteTokens`,
      ),
    }),
  });
}

function parseUsageList(value) {
  if (!Array.isArray(value)) {
    throw new TypeError("usage must be an array");
  }
  return Object.freeze(value.map(parseLaneUsage));
}

function parseAssistantSource(value, index) {
  const label = `source[${index}]`;
  assertExactKeys(
    value,
    ["title", "url", "snippet", "rank", "fetchedAtUnixMs"],
    label,
  );
  const url = parseNonEmptyString(value.url, `${label}.url`);
  let parsedUrl;
  try {
    parsedUrl = new URL(url);
  } catch {
    throw new TypeError(`${label}.url must be a valid URL`);
  }
  if (parsedUrl.protocol !== "https:" && parsedUrl.protocol !== "http:") {
    throw new TypeError(`${label}.url must use HTTP or HTTPS`);
  }
  if (!Number.isSafeInteger(value.rank) || value.rank < 1 || value.rank > 5) {
    throw new TypeError(`${label}.rank must be between 1 and 5`);
  }
  return Object.freeze({
    title: parseNonEmptyString(value.title, `${label}.title`),
    url,
    snippet: parseNonEmptyString(value.snippet, `${label}.snippet`),
    rank: value.rank,
    fetchedAtUnixMs: parseNonNegativeInteger(
      value.fetchedAtUnixMs,
      `${label}.fetchedAtUnixMs`,
    ),
  });
}

function parseAssistantSources(value) {
  if (!Array.isArray(value) || value.length > 5) {
    throw new TypeError("sources must be an array with at most five entries");
  }
  return Object.freeze(value.map(parseAssistantSource));
}

function parseReplyChain(value, index, label = `reply chain[${index}]`) {
  assertExactKeys(value, ["text", "speechText", "visualState"], label);
  return Object.freeze({
    text: parseNonEmptyString(value.text, `${label}.text`),
    speechText: parseString(value.speechText, `${label}.speechText`),
    visualState: parseVisualStateId(value.visualState, `${label}.visualState`),
  });
}

function parseReplyChains(value, label = "reply chains") {
  if (!Array.isArray(value) || value.length < 1 || value.length > 12) {
    throw new TypeError(`${label} must contain 1-12 entries`);
  }
  return Object.freeze(value.map((chain, index) => parseReplyChain(chain, index, `${label}[${index}]`)));
}

export function normalizeCompanionError(error) {
  if (
    error !== null &&
    typeof error === "object" &&
    !Array.isArray(error) &&
    typeof error.code === "string" &&
    error.code.length > 0 &&
    typeof error.message === "string" &&
    error.message.length > 0 &&
    typeof error.retryable === "boolean"
  ) {
    return Object.freeze({
      code: error.code,
      message: error.message,
      retryable: error.retryable,
    });
  }
  return Object.freeze({
    code: "TAURI_INVOKE_FAILED",
    message: "FAIRY 无法完成这次请求，请稍后重试。",
    retryable: true,
  });
}

function parseWireError(value, label) {
  assertExactKeys(value, ["code", "message", "retryable"], label);
  const error = normalizeCompanionError(value);
  if (error.code === "TAURI_INVOKE_FAILED") {
    throw new TypeError(`${label} is invalid`);
  }
  return error;
}

function parseEventPayload(value) {
  assertObject(value, "event.payload");
  switch (value.type) {
    case "state_changed":
      assertExactKeys(value, ["type"], "event.payload");
      return Object.freeze({ type: value.type });
    case "text_delta":
      assertExactKeys(value, ["type", "delta"], "event.payload");
      return Object.freeze({
        type: value.type,
        delta: parseNonEmptyString(value.delta, "event.payload.delta"),
      });
    case "reply_chain":
      assertExactKeys(
        value,
        ["type", "index", "delta", "text", "speechText", "visualState"],
        "event.payload",
      );
      if (!Number.isSafeInteger(value.index) || value.index < 0 || value.index > 11) {
        throw new TypeError("event.payload.index must be between 0 and 11");
      }
      return Object.freeze({
        type: value.type,
        index: value.index,
        delta: parseNonEmptyString(value.delta, "event.payload.delta"),
        text: parseNonEmptyString(value.text, "event.payload.text"),
        speechText: parseString(value.speechText, "event.payload.speechText"),
        visualState: parseVisualStateId(value.visualState, "event.payload.visualState"),
      });
    case "utterance":
      assertExactKeys(
        value,
        ["type", "seq", "text", "visualState", "reason"],
        "event.payload",
      );
      if (!Number.isSafeInteger(value.seq) || value.seq < 0 || value.seq > 255) {
        throw new TypeError("event.payload.seq must be between 0 and 255");
      }
      return Object.freeze({
        type: value.type,
        seq: value.seq,
        text: parseNonEmptyString(value.text, "event.payload.text"),
        visualState: parseVisualStateId(value.visualState, "event.payload.visualState"),
        reason: parseNonEmptyString(value.reason, "event.payload.reason"),
      });
    case "completed":
      assertExactKeys(
        value,
        [
          "type",
          "text",
          "speechText",
          "sources",
          "characterRevision",
          "userProfileRevision",
          "usage",
          "visualState",
          "chains",
        ],
        "event.payload",
      );
      return Object.freeze({
        type: value.type,
        text: parseNonEmptyString(value.text, "event.payload.text"),
        speechText: parseString(value.speechText, "event.payload.speechText"),
        sources: parseAssistantSources(value.sources),
        characterRevision: parseRevision(
          value.characterRevision,
          "event.payload.characterRevision",
        ),
        userProfileRevision: parseOptionalRevision(
          value.userProfileRevision,
          "event.payload.userProfileRevision",
        ),
        usage: parseUsageList(value.usage),
        visualState: parseVisualStateId(value.visualState, "event.payload.visualState"),
        chains: parseReplyChains(value.chains, "event.payload.chains"),
      });
    case "speech.requested":
      assertExactKeys(
        value,
        ["type", "text", "characterRevision", "userProfileRevision"],
        "event.payload",
      );
      return Object.freeze({
        type: value.type,
        text: parseNonEmptyString(value.text, "event.payload.text"),
        characterRevision: parseRevision(
          value.characterRevision,
          "event.payload.characterRevision",
        ),
        userProfileRevision: parseOptionalRevision(
          value.userProfileRevision,
          "event.payload.userProfileRevision",
        ),
      });
    case "speech.synthesized":
      assertExactKeys(
        value,
        ["type", "index", "chainIndex", "text", "speakerId", "mimeType", "format", "dataUrl"],
        "event.payload",
      );
      // index is playback order (monotonic across the turn, utterance audio first).
      if (!Number.isSafeInteger(value.index) || value.index < 0 || value.index > 31) {
        throw new TypeError("event.payload.index must be between 0 and 31");
      }
      // chainIndex is the reply-chain index, or -1 for mid-ReAct utterance audio.
      if (!Number.isSafeInteger(value.chainIndex) || value.chainIndex < -1) {
        throw new TypeError("event.payload.chainIndex must be an integer >= -1");
      }
      if (typeof value.dataUrl !== "string" || !AUDIO_DATA_URL_PATTERN.test(value.dataUrl)) {
        throw new TypeError("event.payload.dataUrl must be an audio data URL");
      }
      return Object.freeze({
        type: value.type,
        index: value.index,
        chainIndex: value.chainIndex,
        text: parseNonEmptyString(value.text, "event.payload.text"),
        speakerId: parseNonEmptyString(value.speakerId, "event.payload.speakerId"),
        mimeType: parseNonEmptyString(value.mimeType, "event.payload.mimeType"),
        format: parseNonEmptyString(value.format, "event.payload.format"),
        dataUrl: value.dataUrl,
      });
    case "speech.failed":
      assertExactKeys(value, ["type", "error"], "event.payload");
      return Object.freeze({
        type: value.type,
        error: parseWireError(value.error, "event.payload.error"),
      });
    case "failed":
      assertExactKeys(value, ["type", "error"], "event.payload");
      return Object.freeze({
        type: value.type,
        error: parseWireError(value.error, "event.payload.error"),
      });
    default:
      throw new TypeError("event.payload.type is unsupported");
  }
}

export function parseHarnessEvent(value) {
  assertExactKeys(
    value,
    ["conversationId", "turnId", "sequence", "state", "payload"],
    "harness event",
  );
  return Object.freeze({
    conversationId: parseUuid(value.conversationId, "event.conversationId"),
    turnId: parseUuid(value.turnId, "event.turnId"),
    sequence: parseRevision(value.sequence, "event.sequence"),
    state: parseTurnState(value.state, "event.state"),
    payload: parseEventPayload(value.payload),
  });
}

export function parseSessionSnapshot(value) {
  assertExactKeys(
    value,
    ["conversationId", "state", "activeTurnId"],
    "session snapshot",
  );
  return Object.freeze({
    conversationId: parseUuid(
      value.conversationId,
      "session snapshot.conversationId",
    ),
    state: parseTurnState(value.state, "session snapshot.state"),
    activeTurnId:
      value.activeTurnId === null
        ? null
        : parseUuid(value.activeTurnId, "session snapshot.activeTurnId"),
  });
}

function parsePersistedMessage(value, index, conversationId) {
  const label = `conversation bootstrap.messages[${index}]`;
  assertExactKeys(
    value,
    ["id", "conversationId", "turnId", "sequence", "role", "content", "createdAtUnixMs"],
    label,
  );
  if (value.conversationId !== conversationId) {
    throw new TypeError(`${label}.conversationId does not match`);
  }
  if (value.role !== "user" && value.role !== "assistant") {
    throw new TypeError(`${label}.role is unsupported`);
  }
  return Object.freeze({
    id: parseUuid(value.id, `${label}.id`),
    conversationId,
    turnId: parseUuid(value.turnId, `${label}.turnId`),
    sequence: parseRevision(value.sequence, `${label}.sequence`),
    role: value.role,
    content: parseNonEmptyString(value.content, `${label}.content`),
    createdAtUnixMs: parseNonNegativeInteger(value.createdAtUnixMs, `${label}.createdAtUnixMs`),
  });
}

export function parseConversationBootstrap(value) {
  assertExactKeys(value, ["conversation", "messages", "promptWindow"], "conversation bootstrap");
  assertExactKeys(
    value.conversation,
    ["id", "characterId", "createdAtUnixMs", "updatedAtUnixMs"],
    "conversation bootstrap.conversation",
  );
  const conversationId = parseUuid(value.conversation.id, "conversation bootstrap.conversation.id");
  const conversation = Object.freeze({
    id: conversationId,
    characterId: parseUuid(value.conversation.characterId, "conversation bootstrap.conversation.characterId"),
    createdAtUnixMs: parseNonNegativeInteger(value.conversation.createdAtUnixMs, "conversation bootstrap.conversation.createdAtUnixMs"),
    updatedAtUnixMs: parseNonNegativeInteger(value.conversation.updatedAtUnixMs, "conversation bootstrap.conversation.updatedAtUnixMs"),
  });
  if (!Array.isArray(value.messages)) {
    throw new TypeError("conversation bootstrap.messages must be an array");
  }
  const messages = Object.freeze(
    value.messages.map((message, index) => parsePersistedMessage(message, index, conversationId)),
  );
  for (let index = 1; index < messages.length; index += 1) {
    if (messages[index - 1].sequence >= messages[index].sequence) {
      throw new TypeError("conversation bootstrap.messages must be strictly ordered");
    }
  }
  assertExactKeys(
    value.promptWindow,
    ["conversationId", "revision", "summary", "cutoffMessageSequence", "updatedAtUnixMs"],
    "conversation bootstrap.promptWindow",
  );
  if (value.promptWindow.conversationId !== conversationId) {
    throw new TypeError("conversation bootstrap.promptWindow.conversationId does not match");
  }
  const promptWindow = Object.freeze({
    conversationId,
    revision: parseRevision(value.promptWindow.revision, "conversation bootstrap.promptWindow.revision"),
    summary: value.promptWindow.summary === null
      ? null
      : parseNonEmptyString(value.promptWindow.summary, "conversation bootstrap.promptWindow.summary"),
    cutoffMessageSequence: parseNonNegativeInteger(
      value.promptWindow.cutoffMessageSequence,
      "conversation bootstrap.promptWindow.cutoffMessageSequence",
    ),
    updatedAtUnixMs: parseNonNegativeInteger(
      value.promptWindow.updatedAtUnixMs,
      "conversation bootstrap.promptWindow.updatedAtUnixMs",
    ),
  });
  return Object.freeze({ conversation, messages, promptWindow });
}

export function parseCharacterActivation(value) {
  assertExactKeys(value, ["character", "session"], "character activation");
  const character = parseCharacter(value.character);
  const session = parseConversationBootstrap(value.session);
  if (session.conversation.characterId !== character.characterId) {
    throw new TypeError("character activation session does not belong to character");
  }
  return Object.freeze({ character, session });
}

export function parseTurnOutcome(value) {
  assertExactKeys(
    value,
    [
      "conversationId",
      "turnId",
      "responseText",
      "speechText",
      "sources",
      "characterRevision",
      "userProfileRevision",
      "usage",
      "speechRequested",
      "visualState",
      "chains",
    ],
    "turn outcome",
  );
  if (typeof value.speechRequested !== "boolean") {
    throw new TypeError("turn outcome.speechRequested must be a boolean");
  }
  return Object.freeze({
    conversationId: parseUuid(
      value.conversationId,
      "turn outcome.conversationId",
    ),
    turnId: parseUuid(value.turnId, "turn outcome.turnId"),
    responseText: parseNonEmptyString(
      value.responseText,
      "turn outcome.responseText",
    ),
    speechText: parseString(value.speechText, "turn outcome.speechText"),
    sources: parseAssistantSources(value.sources),
    characterRevision: parseRevision(
      value.characterRevision,
      "turn outcome.characterRevision",
    ),
    userProfileRevision: parseOptionalRevision(
      value.userProfileRevision,
      "turn outcome.userProfileRevision",
    ),
    usage: parseUsageList(value.usage),
    speechRequested: value.speechRequested,
    visualState: parseVisualStateId(value.visualState, "turn outcome.visualState"),
    chains: parseReplyChains(value.chains, "turn outcome.chains"),
  });
}

export function parseCompactionResult(value) {
  assertExactKeys(
    value,
    ["windowRevision", "retainedDialogueItems"],
    "compaction result",
  );
  if (
    !Number.isSafeInteger(value.retainedDialogueItems) ||
    value.retainedDialogueItems < 0
  ) {
    throw new TypeError(
      "compaction result.retainedDialogueItems must be a non-negative integer",
    );
  }
  return Object.freeze({
    windowRevision: parseRevision(
      value.windowRevision,
      "compaction result.windowRevision",
    ),
    retainedDialogueItems: value.retainedDialogueItems,
  });
}

export function parseCharacter(value) {
  assertExactKeys(
    value,
    ["characterId", "revision", "name", "description", "dialogueStyle", "textLanguage", "speakingLanguage", "appearance"],
    "character",
  );
  return Object.freeze({
    characterId: parseUuid(value.characterId, "character.characterId"),
    revision: parseRevision(value.revision, "character.revision"),
    name: parseNonEmptyString(value.name, "character.name"),
    description: parseNonEmptyString(value.description, "character.description"),
    dialogueStyle: parseOptionalNonEmptyString(value.dialogueStyle, "character.dialogueStyle"),
    textLanguage: parseCharacterSpeakingLanguage(value.textLanguage, "character.textLanguage"),
    speakingLanguage: parseCharacterSpeakingLanguage(value.speakingLanguage, "character.speakingLanguage"),
    appearance: parseCharacterAppearance(value.appearance),
  });
}

const VISUAL_PACK_ID_PATTERN = /^[a-z0-9](?:[a-z0-9.-]{0,62}[a-z0-9])?$/;
const VISUAL_STATE_ID_PATTERN = /^[a-z](?:[a-z0-9_-]{0,30}[a-z0-9])?$/;

function parseBoundedInteger(value, minimum, maximum, label) {
  if (!Number.isSafeInteger(value) || value < minimum || value > maximum) {
    throw new TypeError(`${label} is out of range`);
  }
  return value;
}

function parseVisualPackId(value, label) {
  if (typeof value !== "string" || !VISUAL_PACK_ID_PATTERN.test(value)) {
    throw new TypeError(`${label} must be a visual pack id`);
  }
  return value;
}

function parseVisualStateId(value, label) {
  if (typeof value !== "string" || !VISUAL_STATE_ID_PATTERN.test(value)) {
    throw new TypeError(`${label} must be a visual state id`);
  }
  return value;
}

function parseVisualImagePath(value, label) {
  if (typeof value !== "string" || !isLocalCharacterImagePath(value)) {
    throw new TypeError(`${label} must be a local character PNG path`);
  }
  return value;
}

function isLocalCharacterImagePath(value) {
  if (
    !value.endsWith(".png") ||
    value.includes("\\") ||
    value.includes("?") ||
    value.includes("#")
  ) {
    return false;
  }
  const relative = value.startsWith("/characters/")
    ? value.slice("/characters/".length)
    : value.startsWith("fairy-character://localhost/")
      ? value.slice("fairy-character://localhost/".length)
      : value.startsWith("http://fairy-character.localhost/")
        ? value.slice("http://fairy-character.localhost/".length)
        : null;
  if (relative === null || relative.length === 0) return false;
  const segments = relative.split("/");
  return segments.every((segment) => segment.length > 0 && segment !== "." && segment !== "..");
}

function parseVisualStateImage(value, label) {
  assertExactKeys(value, ["id", "description", "imagePath"], label);
  const description = parseNonEmptyString(value.description, `${label}.description`);
  if (description.trim() !== description || description.length > 96) {
    throw new TypeError(`${label}.description is invalid`);
  }
  return Object.freeze({
    id: parseVisualStateId(value.id, `${label}.id`),
    description,
    imagePath: parseVisualImagePath(value.imagePath, `${label}.imagePath`),
  });
}

export function parseVisualPack(value) {
  assertExactKeys(
    value,
    [
      "schemaVersion",
      "packId",
      "displayName",
      "renderer",
      "frame",
      "scale",
      "anchor",
      "states",
    ],
    "visual pack",
  );
  if (value.schemaVersion !== 2 || value.renderer !== "state_images") {
    throw new TypeError("visual pack schema or renderer is unsupported");
  }
  assertExactKeys(value.frame, ["width", "height"], "visual pack.frame");
  assertExactKeys(value.anchor, ["x", "y"], "visual pack.anchor");
  const frame = Object.freeze({
    width: parseBoundedInteger(value.frame.width, 1, 512, "visual pack.frame.width"),
    height: parseBoundedInteger(value.frame.height, 1, 512, "visual pack.frame.height"),
  });
  const anchor = Object.freeze({
    x: parseBoundedInteger(value.anchor.x, 0, frame.width - 1, "visual pack.anchor.x"),
    y: parseBoundedInteger(value.anchor.y, 0, frame.height - 1, "visual pack.anchor.y"),
  });
  if (!Array.isArray(value.states) || value.states.length < 1 || value.states.length > 16) {
    throw new TypeError("visual pack.states must contain 1-16 entries");
  }
  const states = Object.freeze(value.states.map((state, index) =>
    parseVisualStateImage(state, `visual pack.states[${index}]`),
  ));
  const stateIds = new Set(states.map((state) => state.id));
  const imagePaths = new Set(states.map((state) => state.imagePath));
  if (stateIds.size !== states.length || imagePaths.size !== states.length) {
    throw new TypeError("visual pack states must have unique ids and image paths");
  }
  if (!stateIds.has("idle")) {
    throw new TypeError("visual pack must declare idle state");
  }
  return Object.freeze({
    schemaVersion: 2,
    packId: parseVisualPackId(value.packId, "visual pack.packId"),
    displayName: parseNonEmptyString(value.displayName, "visual pack.displayName"),
    renderer: value.renderer,
    frame,
    scale: parseBoundedInteger(value.scale, 1, 8, "visual pack.scale"),
    anchor,
    states,
  });
}

function parseCharacterAppearance(value) {
  assertObject(value, "character.appearance");
  if (value.status === "unassigned" || value.status === "unavailable") {
    assertExactKeys(value, ["status"], "character.appearance");
    return Object.freeze({ status: value.status });
  }
  if (value.status === "assigned") {
    assertExactKeys(
      value,
      ["status", "bindingRevision", "visual"],
      "character.appearance",
    );
    return Object.freeze({
      status: value.status,
      bindingRevision: parseRevision(
        value.bindingRevision,
        "character.appearance.bindingRevision",
      ),
      visual: parseVisualPack(value.visual),
    });
  }
  throw new TypeError("character.appearance.status is unsupported");
}

export function parseVisualPackCatalog(value) {
  assertExactKeys(value, ["visualPacks"], "visual pack catalog");
  if (!Array.isArray(value.visualPacks)) {
    throw new TypeError("visual pack catalog.visualPacks must be an array");
  }
  const visualPacks = Object.freeze(value.visualPacks.map(parseVisualPack));
  const ids = new Set(visualPacks.map((pack) => pack.packId));
  if (ids.size !== visualPacks.length) {
    throw new TypeError("visual pack catalog contains duplicate ids");
  }
  return Object.freeze({ visualPacks });
}

export function parseCharacterCatalog(value) {
  assertExactKeys(
    value,
    ["characters", "active", "diagnostics"],
    "character catalog",
  );
  if (!Array.isArray(value.characters) || !Array.isArray(value.diagnostics)) {
    throw new TypeError("character catalog lists must be arrays");
  }
  return Object.freeze({
    characters: Object.freeze(value.characters.map(parseCharacter)),
    active: value.active === null ? null : parseCharacter(value.active),
    diagnostics: Object.freeze(
      value.diagnostics.map((diagnostic, index) => {
        const label = `character diagnostic[${index}]`;
        assertExactKeys(
          diagnostic,
          ["characterId", "revision", "code", "message"],
          label,
        );
        return Object.freeze({
          characterId:
            diagnostic.characterId === null
              ? null
              : parseUuid(diagnostic.characterId, `${label}.characterId`),
          revision:
            diagnostic.revision === null
              ? null
              : parseRevision(diagnostic.revision, `${label}.revision`),
          code: parseNonEmptyString(diagnostic.code, `${label}.code`),
          message: parseNonEmptyString(diagnostic.message, `${label}.message`),
        });
      }),
    ),
  });
}

export function parseUserProfile(value) {
  if (value === null) return null;
  assertExactKeys(value, ["revision", "preferredName"], "user profile");
  if (
    value.preferredName !== null &&
    (typeof value.preferredName !== "string" || value.preferredName.length === 0)
  ) {
    throw new TypeError("user profile.preferredName must be null or non-empty");
  }
  return Object.freeze({
    revision: parseRevision(value.revision, "user profile.revision"),
    preferredName: value.preferredName,
  });
}

export function parseUserProfileUpdate(value) {
  assertExactKeys(
    value,
    ["profile", "changed", "recoveredCorruption"],
    "user profile update",
  );
  if (
    typeof value.changed !== "boolean" ||
    typeof value.recoveredCorruption !== "boolean"
  ) {
    throw new TypeError("user profile update flags must be booleans");
  }
  return Object.freeze({
    profile: parseUserProfile(value.profile),
    changed: value.changed,
    recoveredCorruption: value.recoveredCorruption,
  });
}

export function parseModelConnectionStatus(value) {
  assertExactKeys(
    value,
    ["configured", "ready", "config", "error"],
    "model status",
  );
  if (typeof value.configured !== "boolean" || typeof value.ready !== "boolean") {
    throw new TypeError("model status flags must be booleans");
  }
  let config = null;
  if (value.config !== null) {
    assertExactKeys(
      value.config,
      ["protocol", "endpoint", "model", "contextWindowTokens", "authMode"],
      "model status.config",
    );
    if (
      !["responses", "chat_completions"].includes(value.config.protocol) ||
      !["bearer_key", "no_auth"].includes(value.config.authMode)
    ) {
      throw new TypeError("model status.config has invalid protocol or auth mode");
    }
    config = Object.freeze({
      protocol: value.config.protocol,
      endpoint: parseNonEmptyString(
        value.config.endpoint,
        "model status.config.endpoint",
      ),
      model: parseNonEmptyString(value.config.model, "model status.config.model"),
      contextWindowTokens: parseContextWindowTokens(
        value.config.contextWindowTokens,
        "model status.config.contextWindowTokens",
      ),
      authMode: value.config.authMode,
    });
  }
  return Object.freeze({
    configured: value.configured,
    ready: value.ready,
    config,
    error:
      value.error === null
        ? null
        : parseWireError(value.error, "model status.error"),
  });
}

export function parseIntelligenceStatus(value) {
  assertExactKeys(
    value,
    [
      "ready",
      "schemaVersion",
      "summary",
      "activeBackgroundJobs",
      "error",
    ],
    "intelligence status",
  );
  if (typeof value.ready !== "boolean") {
    throw new TypeError("intelligence status.ready must be a boolean");
  }
  const schemaVersion = value.schemaVersion === null
    ? null
    : parseRevision(value.schemaVersion, "intelligence status.schemaVersion");
  let summary = null;
  if (value.summary !== null) {
    assertExactKeys(
      value.summary,
      [
        "conversations",
        "activeGlobalMemories",
        "activeCharacterMemories",
        "needsReviewMemories",
        "pendingExtractionTurns",
        "runningBatches",
        "failedBatches",
        "candidateKnowledge",
        "verifiedKnowledge",
      ],
      "intelligence status.summary",
    );
    summary = Object.freeze({
      conversations: parseNonNegativeInteger(value.summary.conversations, "intelligence status.summary.conversations"),
      activeGlobalMemories: parseNonNegativeInteger(value.summary.activeGlobalMemories, "intelligence status.summary.activeGlobalMemories"),
      activeCharacterMemories: parseNonNegativeInteger(value.summary.activeCharacterMemories, "intelligence status.summary.activeCharacterMemories"),
      needsReviewMemories: parseNonNegativeInteger(value.summary.needsReviewMemories, "intelligence status.summary.needsReviewMemories"),
      pendingExtractionTurns: parseNonNegativeInteger(value.summary.pendingExtractionTurns, "intelligence status.summary.pendingExtractionTurns"),
      runningBatches: parseNonNegativeInteger(value.summary.runningBatches, "intelligence status.summary.runningBatches"),
      failedBatches: parseNonNegativeInteger(value.summary.failedBatches, "intelligence status.summary.failedBatches"),
      candidateKnowledge: parseNonNegativeInteger(
        value.summary.candidateKnowledge,
        "intelligence status.summary.candidateKnowledge",
      ),
      verifiedKnowledge: parseNonNegativeInteger(
        value.summary.verifiedKnowledge,
        "intelligence status.summary.verifiedKnowledge",
      ),
    });
  }
  return Object.freeze({
    ready: value.ready,
    schemaVersion,
    summary,
    activeBackgroundJobs: parseNonNegativeInteger(
      value.activeBackgroundJobs,
      "intelligence status.activeBackgroundJobs",
    ),
    error:
      value.error === null
        ? null
        : parseWireError(value.error, "intelligence status.error"),
  });
}

function parseKnowledgeRecord(value, index, expectedStatus) {
  const label = `${expectedStatus} knowledge[${index}]`;
  assertExactKeys(
    value,
    [
      "id",
      "topic",
      "statement",
      "status",
      "verificationBasis",
      "confidenceBasisPoints",
      "sourceConversationId",
      "sourceTurnId",
      "supersedesId",
      "sources",
      "createdAtUnixMs",
      "updatedAtUnixMs",
    ],
    label,
  );
  if (value.status !== expectedStatus) {
    throw new TypeError(`${label}.status must be ${expectedStatus}`);
  }
  if (!Number.isSafeInteger(value.confidenceBasisPoints)
    || value.confidenceBasisPoints < 0
    || value.confidenceBasisPoints > 10_000) {
    throw new TypeError(`${label}.confidenceBasisPoints must be between 0 and 10000`);
  }
  const sources = parseAssistantSources(value.sources);
  if (expectedStatus === "candidate") {
    if (value.verificationBasis !== "unverified" || sources.length !== 0) {
      throw new TypeError(`${label} must be unverified and source-free`);
    }
  } else if (value.verificationBasis === "web_source") {
    if (sources.length === 0) {
      throw new TypeError(`${label} with web_source must include sources`);
    }
  } else if (value.verificationBasis === "user_confirmed") {
    if (sources.length !== 0) {
      throw new TypeError(`${label} with user_confirmed must be source-free`);
    }
  } else {
    throw new TypeError(`${label}.verificationBasis is unsupported`);
  }
  return Object.freeze({
    id: parseUuid(value.id, `${label}.id`),
    topic: parseNonEmptyString(value.topic, `${label}.topic`),
    statement: parseNonEmptyString(value.statement, `${label}.statement`),
    status: value.status,
    verificationBasis: value.verificationBasis,
    confidenceBasisPoints: value.confidenceBasisPoints,
    sourceConversationId: parseUuid(
      value.sourceConversationId,
      `${label}.sourceConversationId`,
    ),
    sourceTurnId: parseUuid(value.sourceTurnId, `${label}.sourceTurnId`),
    supersedesId: value.supersedesId === null
      ? null
      : parseUuid(value.supersedesId, `${label}.supersedesId`),
    sources,
    createdAtUnixMs: parseNonNegativeInteger(
      value.createdAtUnixMs,
      `${label}.createdAtUnixMs`,
    ),
    updatedAtUnixMs: parseNonNegativeInteger(
      value.updatedAtUnixMs,
      `${label}.updatedAtUnixMs`,
    ),
  });
}

export function parseKnowledgeCatalog(value) {
  assertExactKeys(value, ["candidates", "verified"], "knowledge catalog");
  if (!Array.isArray(value.candidates) || value.candidates.length > 20) {
    throw new TypeError("knowledge catalog.candidates must contain at most 20 entries");
  }
  if (!Array.isArray(value.verified) || value.verified.length > 20) {
    throw new TypeError("knowledge catalog.verified must contain at most 20 entries");
  }
  return Object.freeze({
    candidates: Object.freeze(
      value.candidates.map((record, index) => parseKnowledgeRecord(record, index, "candidate")),
    ),
    verified: Object.freeze(
      value.verified.map((record, index) => parseKnowledgeRecord(record, index, "verified")),
    ),
  });
}

export function parseConfirmedKnowledgeRecord(value) {
  return parseKnowledgeRecord(value, 0, "verified");
}

const MEMORY_KINDS = new Set(["preference", "profile", "relationship", "experience"]);

function parseMemoryScope(value, label) {
  assertObject(value, label);
  if (value.type === "global" || value.type === "unassigned_legacy") {
    assertExactKeys(value, ["type"], label);
    return Object.freeze({ type: value.type });
  }
  if (value.type === "character") {
    assertExactKeys(value, ["type", "characterId"], label);
    return Object.freeze({
      type: value.type,
      characterId: parseUuid(value.characterId, `${label}.characterId`),
    });
  }
  throw new TypeError(`${label}.type is unsupported`);
}

function parsePersonalMemoryRecord(value, index, labelPrefix = "personal memory") {
  const label = `${labelPrefix}[${index}]`;
  assertExactKeys(value, [
    "id", "kind", "scope", "reviewStatus", "content", "status",
    "confidenceBasisPoints", "sourceConversationId", "sourceTurnId",
    "supersedesId", "createdAtUnixMs", "updatedAtUnixMs",
  ], label);
  if (!MEMORY_KINDS.has(value.kind)) throw new TypeError(`${label}.kind is unsupported`);
  if (!["ready", "needs_review"].includes(value.reviewStatus)) {
    throw new TypeError(`${label}.reviewStatus is unsupported`);
  }
  if (!["active", "superseded", "tombstone"].includes(value.status)) {
    throw new TypeError(`${label}.status is unsupported`);
  }
  const confidence = parseNonNegativeInteger(value.confidenceBasisPoints, `${label}.confidenceBasisPoints`);
  if (confidence > 10000) throw new TypeError(`${label}.confidenceBasisPoints is too large`);
  return Object.freeze({
    id: parseUuid(value.id, `${label}.id`),
    kind: value.kind,
    scope: parseMemoryScope(value.scope, `${label}.scope`),
    reviewStatus: value.reviewStatus,
    content: parseNonEmptyString(value.content, `${label}.content`),
    status: value.status,
    confidenceBasisPoints: confidence,
    sourceConversationId: parseUuid(value.sourceConversationId, `${label}.sourceConversationId`),
    sourceTurnId: parseUuid(value.sourceTurnId, `${label}.sourceTurnId`),
    supersedesId: value.supersedesId === null ? null : parseUuid(value.supersedesId, `${label}.supersedesId`),
    createdAtUnixMs: parseNonNegativeInteger(value.createdAtUnixMs, `${label}.createdAtUnixMs`),
    updatedAtUnixMs: parseNonNegativeInteger(value.updatedAtUnixMs, `${label}.updatedAtUnixMs`),
  });
}

export function parsePersonalMemory(value) {
  return parsePersonalMemoryRecord(value, 0);
}

export function parsePersonalMemoryCatalog(value) {
  assertExactKeys(value, ["global", "character", "needsReview"], "personal memory catalog");
  for (const key of ["global", "character", "needsReview"]) {
    if (!Array.isArray(value[key])) throw new TypeError(`personal memory catalog.${key} must be an array`);
  }
  return Object.freeze({
    global: Object.freeze(value.global.map((record, index) => parsePersonalMemoryRecord(record, index, "global memory"))),
    character: Object.freeze(value.character.map((record, index) => parsePersonalMemoryRecord(record, index, "character memory"))),
    needsReview: Object.freeze(value.needsReview.map((record, index) => parsePersonalMemoryRecord(record, index, "review memory"))),
  });
}

function parseExtractionBatchRecord(value, index, expectedStatus) {
  const label = `${expectedStatus} batch[${index}]`;
  assertExactKeys(value, [
    "id", "conversationId", "characterId", "status", "firstTurnSequence",
    "lastTurnSequence", "error", "createdAtUnixMs", "updatedAtUnixMs",
  ], label);
  if (value.status !== expectedStatus) throw new TypeError(`${label}.status does not match`);
  return Object.freeze({
    id: parseUuid(value.id, `${label}.id`),
    conversationId: parseUuid(value.conversationId, `${label}.conversationId`),
    characterId: parseUuid(value.characterId, `${label}.characterId`),
    status: value.status,
    firstTurnSequence: parseRevision(value.firstTurnSequence, `${label}.firstTurnSequence`),
    lastTurnSequence: parseRevision(value.lastTurnSequence, `${label}.lastTurnSequence`),
    error: value.error === null ? null : parseWireError(value.error, `${label}.error`),
    createdAtUnixMs: parseNonNegativeInteger(value.createdAtUnixMs, `${label}.createdAtUnixMs`),
    updatedAtUnixMs: parseNonNegativeInteger(value.updatedAtUnixMs, `${label}.updatedAtUnixMs`),
  });
}

export function parseExtractionBatchCatalog(value) {
  assertExactKeys(value, ["running", "failed"], "extraction batch catalog");
  if (!Array.isArray(value.running) || !Array.isArray(value.failed)) {
    throw new TypeError("extraction batch catalog lists must be arrays");
  }
  return Object.freeze({
    running: Object.freeze(value.running.map((record, index) => parseExtractionBatchRecord(record, index, "running"))),
    failed: Object.freeze(value.failed.map((record, index) => parseExtractionBatchRecord(record, index, "failed"))),
  });
}

export function createCompanionState() {
  return Object.freeze({
    conversationId: null,
    characterId: null,
    sessionState: "idle",
    activeTurnId: null,
    terminalTurn: null,
    lastSequence: 0,
    draft: "",
    responseDraft: "",
    progressiveDraft: "",
    transcript: Object.freeze([]),
    error: null,
    usage: Object.freeze([]),
    speechRequest: null,
    settingsStatus: null,
    submitting: false,
  });
}

function protocolError(message) {
  throw new TypeError(`companion event protocol: ${message}`);
}

function readCompiledOutcome(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError("compiled turn outcome must be an object");
  }
  if (typeof value.conversationId !== "string" || value.conversationId.length === 0) {
    throw new TypeError("compiled turn outcome.conversationId must be a non-empty string");
  }
  if (typeof value.turnId !== "string" || value.turnId.length === 0) {
    throw new TypeError("compiled turn outcome.turnId must be a non-empty string");
  }
  if (typeof value.responseText !== "string") {
    throw new TypeError("compiled turn outcome.responseText must be a string");
  }
  if (typeof value.speechText !== "string") {
    throw new TypeError("compiled turn outcome.speechText must be a string");
  }
  if (typeof value.speechRequested !== "boolean") {
    throw new TypeError("compiled turn outcome.speechRequested must be a boolean");
  }
  if (!Array.isArray(value.chains)) {
    throw new TypeError("compiled turn outcome.chains must be an array");
  }
  return value;
}

function completedTerminalFromOutcome(outcome) {
  return Object.freeze({
    turnId: outcome.turnId,
    state: "completed",
    text: outcome.responseText,
    speechText: outcome.speechText,
    sources: Array.isArray(outcome.sources) ? Object.freeze(outcome.sources) : Object.freeze([]),
    chains: outcome.chains,
    characterRevision: Number.isSafeInteger(outcome.characterRevision) && outcome.characterRevision > 0
      ? outcome.characterRevision
      : 1,
    userProfileRevision: outcome.userProfileRevision == null
      ? null
      : outcome.userProfileRevision,
    // Marks RPC-first completion so leftover mid-stream harness events can drain
    // without breaking the events-first path (which must still reject stray deltas).
    reconciled: true,
  });
}

/**
 * Apply a compiled SubmitTurn outcome when harness streaming may have missed
 * `completed`, or when the RPC returned before the frontend processed it.
 * Idempotent against an already-completed terminal for the same turn so the
 * common "events first, then RPC" order is a no-op.
 */
function applyCompiledTurnOutcome(state, rawOutcome, { soft = false } = {}) {
  const outcome = soft ? readCompiledOutcome(rawOutcome) : parseTurnOutcome(rawOutcome);
  if (state.conversationId !== outcome.conversationId) {
    if (soft) return state;
    throw new TypeError("compiled turn outcome conversation id does not match");
  }
  if (state.terminalTurn?.turnId === outcome.turnId && state.terminalTurn.state === "completed") {
    return state;
  }
  if (!state.submitting) {
    // Failed/interrupted/idle after await: never throw into React from the
    // submit finally-path; the harness path already owns the terminal state.
    if (soft) return state;
    throw new TypeError("compiled turn outcome has no pending submission");
  }
  const transcript = state.transcript.map((entry, index) => {
    if (index !== state.transcript.length - 1 || entry.role !== "user") return entry;
    return Object.freeze({ ...entry, turnId: outcome.turnId });
  });
  transcript.push(Object.freeze({
    role: "assistant",
    text: outcome.responseText,
    speechText: outcome.speechText,
    sources: Array.isArray(outcome.sources) ? outcome.sources : Object.freeze([]),
    chains: outcome.chains,
    status: "completed",
    turnId: outcome.turnId,
  }));
  return Object.freeze({
    ...state,
    sessionState: "completed",
    activeTurnId: null,
    terminalTurn: completedTerminalFromOutcome(outcome),
    responseDraft: "",
    progressiveDraft: "",
    transcript: Object.freeze(transcript),
    error: null,
    usage: Array.isArray(outcome.usage) ? outcome.usage : Object.freeze([]),
    speechRequest: outcome.speechRequested && outcome.speechText.length > 0
      ? Object.freeze({ text: outcome.speechText, turnId: outcome.turnId })
      : null,
    submitting: false,
  });
}

function acknowledgeLateCompleted(state, event) {
  const payload = event.payload;
  const terminal = state.terminalTurn;
  const enriched = Object.freeze({
    turnId: terminal.turnId,
    state: "completed",
    text: typeof terminal.text === "string" ? terminal.text : payload.text,
    speechText: typeof terminal.speechText === "string" ? terminal.speechText : payload.speechText,
    sources: Array.isArray(terminal.sources) ? terminal.sources : payload.sources,
    chains: Array.isArray(terminal.chains) ? terminal.chains : payload.chains,
    characterRevision: Number.isSafeInteger(terminal.characterRevision)
      ? terminal.characterRevision
      : payload.characterRevision,
    userProfileRevision: Object.prototype.hasOwnProperty.call(terminal, "userProfileRevision")
      ? terminal.userProfileRevision
      : payload.userProfileRevision,
    // Once harness completed is seen, further stray stream events should not drain.
    reconciled: false,
  });
  const usage = Array.isArray(state.usage) && state.usage.length > 0
    ? state.usage
    : payload.usage;
  return Object.freeze({
    ...state,
    terminalTurn: enriched,
    lastSequence: event.sequence,
    usage,
    progressiveDraft: "",
    error: null,
  });
}

function reduceHarnessEvent(state, event) {
  if (state.conversationId !== event.conversationId) {
    protocolError("conversation id does not match the active session");
  }

  if (event.sequence !== state.lastSequence + 1) {
    // After submit_started, lastSequence resets to 0 while late events from the
    // previous turn may still arrive on the permanent harness listener. Drop them.
    if (state.submitting && state.activeTurnId === null && event.sequence !== 1) {
      return state;
    }
    // Late events from another turn after this turn already started.
    if (state.activeTurnId !== null && event.turnId !== state.activeTurnId) {
      return state;
    }
    // Duplicate delivery (e.g. overlapping listeners): ignore already-applied seq.
    if (
      state.activeTurnId !== null &&
      event.turnId === state.activeTurnId &&
      event.sequence <= state.lastSequence
    ) {
      return state;
    }
    // RPC-first reconcile freezes lastSequence mid-stream. Allow same-turn
    // completed/speech to jump forward when intermediate events are still queued
    // or were drained out-of-band by speech playback.
    if (
      state.terminalTurn?.reconciled === true &&
      state.terminalTurn.state === "completed" &&
      event.turnId === state.terminalTurn.turnId &&
      event.sequence > state.lastSequence &&
      (
        event.payload.type === "completed" ||
        event.payload.type === "speech.requested" ||
        event.payload.type === "speech.synthesized" ||
        event.payload.type === "speech.failed"
      )
    ) {
      // fall through
    } else if (
      state.terminalTurn?.state === "completed" &&
      event.turnId === state.terminalTurn.turnId &&
      event.sequence <= state.lastSequence
    ) {
      return state;
    } else {
      protocolError("event sequence is duplicated or out of order");
    }
  }

  if (state.terminalTurn !== null) {
    // RPC reconcile can win the race against a late harness `completed`. Treat
    // same-turn completed as an idempotent ack / metadata enrich, never a crash.
    if (
      event.payload.type === "completed" &&
      state.terminalTurn.state === "completed" &&
      event.turnId === state.terminalTurn.turnId &&
      event.state === "completed"
    ) {
      return acknowledgeLateCompleted(state, event);
    }
    const speech = event.payload.type === "speech.requested" || event.payload.type === "speech.synthesized" || event.payload.type === "speech.failed";
    if (
      speech &&
      state.terminalTurn.state === "completed" &&
      event.turnId === state.terminalTurn.turnId &&
      event.state === "completed"
    ) {
      if (event.payload.type === "speech.synthesized" || event.payload.type === "speech.failed") {
        return Object.freeze({
          ...state,
          lastSequence: event.sequence,
        });
      }
      if (state.speechRequest !== null) {
        // Already set by reconcile or an earlier speech.requested — ignore duplicates.
        return Object.freeze({
          ...state,
          lastSequence: event.sequence,
        });
      }
      const completed = state.terminalTurn;
      if (
        event.payload.text !== completed.speechText ||
        event.payload.characterRevision !== completed.characterRevision ||
        event.payload.userProfileRevision !== completed.userProfileRevision
      ) {
        protocolError("speech request does not match the completed response");
      }
      return Object.freeze({
        ...state,
        lastSequence: event.sequence,
        speechRequest: event.payload,
      });
    }
    // After RPC-first reconcile only: leftover same-turn stream events
    // (reply_chain, state_changed, …) advance lastSequence so later
    // completed/speech stay ordered. Events-first terminals still reject strays.
    if (
      state.terminalTurn.reconciled === true &&
      state.terminalTurn.state === "completed" &&
      event.turnId === state.terminalTurn.turnId
    ) {
      return Object.freeze({
        ...state,
        lastSequence: event.sequence,
      });
    }
    protocolError("terminal turn cannot accept this event");
  }

  const activeTurnId = state.activeTurnId ?? event.turnId;
  if (event.turnId !== activeTurnId) {
    // Ignore leftover events from a previous turn instead of crashing the UI.
    return state;
  }

  if (event.payload.type === "state_changed") {
    if (event.state === "completed" || event.state === "failed") {
      protocolError("completed and failed states require typed terminal payloads");
    }
    if (event.state === "interrupted") {
      if (!["interpreting", "gathering", "planning", "responding"].includes(state.sessionState)) {
        protocolError("interrupted state has no active predecessor");
      }
      return Object.freeze({
        ...state,
        sessionState: event.state,
        activeTurnId: null,
        terminalTurn: Object.freeze({
          turnId: event.turnId,
          state: event.state,
        }),
        lastSequence: event.sequence,
        submitting: false,
      });
    }
    const expectedState = {
      idle: "interpreting",
      interpreting: "gathering",
      gathering: "planning",
      planning: "responding",
    }[state.sessionState];
    if (event.state !== expectedState) {
      protocolError("state transition is invalid");
    }
    return Object.freeze({
      ...state,
      sessionState: event.state,
      activeTurnId,
      lastSequence: event.sequence,
    });
  }

  if (event.payload.type === "utterance") {
    if (event.state !== "planning" && event.state !== "gathering") {
      protocolError("utterance must be in gathering or planning state");
    }
    const prefix = state.progressiveDraft.length > 0 ? `${state.progressiveDraft}\n` : "";
    return Object.freeze({
      ...state,
      sessionState: event.state,
      activeTurnId,
      lastSequence: event.sequence,
      progressiveDraft: prefix + event.payload.text,
    });
  }

  if (event.payload.type === "text_delta") {
    if (event.state !== "responding") {
      protocolError("text delta must be in responding state");
    }
    return Object.freeze({
      ...state,
      sessionState: event.state,
      activeTurnId,
      lastSequence: event.sequence,
      responseDraft: state.responseDraft + event.payload.delta,
    });
  }

  if (event.payload.type === "reply_chain") {
    if (event.state !== "responding") {
      protocolError("reply chain must be in responding state");
    }
    let progressiveDraft = state.progressiveDraft;
    if (
      progressiveDraft.length > 0 &&
      progressiveDraft.split("\n").at(-1) === event.payload.text
    ) {
      // Final line duplicates last utterance — keep progressive as-is for bubble continuity.
      progressiveDraft = state.progressiveDraft;
    }
    return Object.freeze({
      ...state,
      sessionState: event.state,
      activeTurnId,
      lastSequence: event.sequence,
      progressiveDraft,
      responseDraft: state.responseDraft + event.payload.delta,
    });
  }

  if (event.payload.type === "completed") {
    if (event.state !== "completed") {
      protocolError("completed payload must use completed state");
    }
    if (state.sessionState !== "responding") {
      protocolError("completed payload has no responding predecessor");
    }
    if (
      state.responseDraft.length > 0 &&
      state.responseDraft !== event.payload.text
    ) {
      protocolError("completed text does not match streamed text");
    }
    const assistant = Object.freeze({
      role: "assistant",
      text: event.payload.text,
      speechText: event.payload.speechText,
      sources: event.payload.sources,
      chains: event.payload.chains,
      status: "completed",
      turnId: event.turnId,
    });
    return Object.freeze({
      ...state,
      sessionState: event.state,
      activeTurnId: null,
      terminalTurn: Object.freeze({
        turnId: event.turnId,
        state: event.state,
        text: event.payload.text,
        speechText: event.payload.speechText,
        sources: event.payload.sources,
        chains: event.payload.chains,
        characterRevision: event.payload.characterRevision,
        userProfileRevision: event.payload.userProfileRevision,
      }),
      lastSequence: event.sequence,
      responseDraft: "",
      progressiveDraft: "",
      speechRequest: null,
      transcript: Object.freeze([...state.transcript, assistant]),
      usage: event.payload.usage,
      submitting: false,
    });
  }

  if (event.payload.type === "failed") {
    if (event.state !== "failed") {
      protocolError("failed payload must use failed state");
    }
    if (!["interpreting", "gathering", "planning", "responding"].includes(state.sessionState)) {
      protocolError("failed payload has no active predecessor");
    }
    return Object.freeze({
      ...state,
      sessionState: event.state,
      activeTurnId: null,
      terminalTurn: Object.freeze({
        turnId: event.turnId,
        state: event.state,
      }),
      lastSequence: event.sequence,
      error: event.payload.error,
      submitting: false,
    });
  }

  if (
    event.payload.type === "speech.requested" ||
    event.payload.type === "speech.synthesized" ||
    event.payload.type === "speech.failed"
  ) {
    if (event.state !== "planning" && event.state !== "responding") {
      protocolError("in-flight speech events outside planning/responding require a completed terminal turn");
    }
    return Object.freeze({
      ...state,
      lastSequence: event.sequence,
      speechRequest: event.payload.type === "speech.requested" ? event.payload : state.speechRequest,
    });
  }

  protocolError("unsupported harness payload for active turn");
}

export function reduceCompanionState(state, action) {
  switch (action.type) {
    case "session_created": {
      const session = parseConversationBootstrap(action.session);
      const transcript = Object.freeze(session.messages.map((message) => Object.freeze({
        role: message.role,
        text: message.content,
        speechText: message.role === "assistant" ? message.content : undefined,
        sources: message.role === "assistant" ? Object.freeze([]) : undefined,
        status: "completed",
        turnId: message.turnId,
      })));
      return Object.freeze({
        ...createCompanionState(),
        conversationId: session.conversation.id,
        characterId: session.conversation.characterId,
        transcript,
      });
    }
    case "session_cleared":
      return createCompanionState();
    case "draft_changed":
      if (typeof action.value !== "string") {
        throw new TypeError("draft value must be a string");
      }
      return Object.freeze({ ...state, draft: action.value });
    case "submit_started": {
      if (state.conversationId === null) {
        throw new TypeError("a session is required before submitting");
      }
      if (state.activeTurnId !== null || state.submitting) {
        throw new TypeError("a turn is already active");
      }
      if (typeof action.text !== "string" || action.text.trim().length === 0) {
        throw new TypeError("对话内容不能为空");
      }
      const user = Object.freeze({
        role: "user",
        text: action.text.trim(),
        status: "completed",
        turnId: null,
      });
      return Object.freeze({
        ...state,
        sessionState: "idle",
        draft: "",
        responseDraft: "",
        transcript: Object.freeze([...state.transcript, user]),
        activeTurnId: null,
        terminalTurn: null,
        lastSequence: 0,
        error: null,
        usage: Object.freeze([]),
        speechRequest: null,
        submitting: true,
      });
    }
    case "harness_event": {
      // Harness events flow through React's reducer, so any throw here escapes to
      // the RootErrorBoundary and takes the whole window down. Ordering, duplicate,
      // and stale-turn anomalies are recoverable: log and drop the offending event
      // instead of crashing the UI.
      try {
        // listenWailsHarnessEvents already parses; accept either wire or parsed shapes.
        const event = action.event?.payload && typeof action.event.sequence === "number"
          ? action.event
          : parseHarnessEvent(action.event);
        return reduceHarnessEvent(state, event);
      } catch (error) {
        if (typeof console !== "undefined" && typeof console.warn === "function") {
          console.warn("dropped malformed companion harness event", error);
        }
        return state;
      }
    }
    case "compiled_turn_completed":
      return applyCompiledTurnOutcome(state, action.outcome, { soft: false });
    case "compiled_turn_reconciled":
      // Soft: safe to always dispatch after await SubmitTurn. Events-first is a
      // no-op; RPC-first fills a rich terminalTurn so late harness completed /
      // speech.requested can ack without protocol errors.
      return applyCompiledTurnOutcome(state, action.outcome, { soft: true });
    case "invoke_failed":
      return Object.freeze({
        ...state,
        activeTurnId: null,
        submitting: false,
        error: normalizeCompanionError(action.error),
      });
    case "settings_status":
      return Object.freeze({ ...state, settingsStatus: action.value });
    default:
      throw new TypeError(`unsupported companion action: ${String(action.type)}`);
  }
}
