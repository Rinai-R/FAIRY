import {
  parseCharacter,
  parseCharacterCatalog,
  parseCompactionResult,
  parseConfirmedKnowledgeRecord,
  parseExtractionBatchCatalog,
  parseHarnessEvent,
  parseKnowledgeCatalog,
  parsePersonalMemory,
  parsePersonalMemoryCatalog,
  parseVisualPackCatalog,
  parseUserProfile,
  parseUserProfileUpdate,
} from "./companionState.mjs";

export async function loadWailsBootstrap(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const status = await bindings.BootstrapService.Status();
  return parseBootstrapStatus(status);
}

export async function loadWailsCharacterCatalog(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const catalog = await bindings.CharacterService.ListCharacters();
  return parseCharacterCatalog(catalog);
}

export async function loadWailsVisualPackCatalog(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const catalog = await bindings.VisualService.ListVisualPacks();
  return parseVisualPackCatalog(catalog);
}

export async function createWailsCharacter(brief, visualPackId, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const character = await bindings.CharacterService.CreateCharacter(brief, visualPackId);
  return parseCharacter(character);
}

export async function updateWailsCharacter(characterId, brief, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const character = await bindings.CharacterService.UpdateCharacter(characterId, brief);
  return parseCharacter(character);
}

export async function setWailsCharacterAppearance(characterId, visualPackId, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const character = await bindings.CharacterService.SetCharacterAppearance(characterId, visualPackId);
  return parseCharacter(character);
}

export async function activateWailsCharacter(characterId, revision, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const character = await bindings.CharacterService.ActivateCharacter(characterId, revision);
  return parseCharacter(character);
}

export async function importWailsCharacterPackage(packagePath, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const character = await bindings.CharacterService.ImportCharacterPackage(packagePath);
  return parseCharacter(character);
}

export async function exportWailsCharacterPackage(characterId, outputPath, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  await bindings.CharacterService.ExportCharacterPackage(characterId, outputPath);
}

export async function loadWailsModelStatus(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const status = await bindings.ConfigService.ModelStatus();
  return parseModelConnectionStatus(status);
}

export async function loadWailsUserProfile(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const profile = await bindings.ProfileService.Current();
  return parseUserProfile(profile);
}

export async function setWailsUserProfile(preferredName, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const update = await bindings.ProfileService.SetPreferredName(preferredName);
  return parseUserProfileUpdate(update);
}

export async function clearWailsUserProfile(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const update = await bindings.ProfileService.Clear();
  return parseUserProfileUpdate(update);
}

export async function saveWailsModelConnection(input, apiKey = null, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const status = await bindings.ConfigService.SaveModelConnection(input, apiKey);
  return parseModelConnectionStatus(status);
}

export async function clearWailsModelConnection(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const status = await bindings.ConfigService.ClearModelConnection();
  return parseModelConnectionStatus(status);
}

export function parseWebSearchStatus(status) {
  if (status === null || typeof status !== "object" || Array.isArray(status)) {
    throw new TypeError("web search status must be an object");
  }
  return Object.freeze({
    enabled: Boolean(status.enabled),
    binaryPath: typeof status.binaryPath === "string" ? status.binaryPath : "",
    binaryFound: Boolean(status.binaryFound),
  });
}

export async function loadWailsWebSearchStatus(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  return parseWebSearchStatus(await bindings.ConfigService.WebSearchStatus());
}

export async function setWailsWebSearchEnabled(enabled, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  return parseWebSearchStatus(await bindings.ConfigService.SetWebSearchEnabled(Boolean(enabled)));
}

export async function loadWailsModelRequestDraft(request, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const draft = await bindings.ModelService.BuildRequestDraft(request);
  return parseModelRequestDraft(draft);
}

export async function loadWailsMemorySummary(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const summary = await bindings.MemoryService.Summary();
  return parseMemorySummary(summary);
}

export async function loadWailsActiveBackgroundJobs(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const value = await bindings.CompanionService.ActiveBackgroundJobs();
  const jobs = Number(value);
  if (!Number.isInteger(jobs) || jobs < 0) {
    throw new Error(`activeBackgroundJobs must be a non-negative integer, got ${JSON.stringify(value)}`);
  }
  return jobs;
}

export async function loadWailsPersonalMemoryCatalog(characterId, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const catalog = await bindings.MemoryService.PersonalMemoryCatalog(characterId);
  return parsePersonalMemoryCatalog(catalog);
}

export async function createWailsPersonalMemory({ kind, scope, content, confidenceBasisPoints = 9000 }, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const record = await bindings.MemoryService.CreatePersonalMemory(kind, scope, content, confidenceBasisPoints);
  return parsePersonalMemory(record);
}

export async function reviseWailsPersonalMemory(id, content, confidenceBasisPoints = 9000, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const record = await bindings.MemoryService.RevisePersonalMemory(id, content, confidenceBasisPoints);
  return parsePersonalMemory(record);
}

export async function tombstoneWailsPersonalMemory(id, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  await bindings.MemoryService.TombstonePersonalMemory(id);
}

export async function assignWailsLegacyRelationship(id, characterId, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const record = await bindings.MemoryService.AssignLegacyRelationship(id, characterId);
  return parsePersonalMemory(record);
}

export async function loadWailsKnowledgeCatalog(loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const catalog = await bindings.MemoryService.KnowledgeCatalog();
  return parseKnowledgeCatalog(catalog);
}

export async function confirmWailsKnowledgeCandidate(id, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const record = await bindings.MemoryService.ConfirmKnowledgeCandidate(id);
  return parseConfirmedKnowledgeRecord(record);
}

export async function tombstoneWailsKnowledge(id, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  await bindings.MemoryService.TombstoneKnowledge(id);
}

export async function loadWailsExtractionBatchCatalog(characterId, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const catalog = await bindings.MemoryService.ExtractionBatchCatalog(characterId);
  return parseExtractionBatchCatalog(catalog);
}

export async function retryWailsExtractionBatch(id, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  await bindings.MemoryService.RetryExtractionBatch(id);
}

export async function createWailsCompanionSession(characterId, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const session = await bindings.MemoryService.OpenOrCreateCharacterConversation(characterId);
  return parseConversationBootstrap(session);
}

export async function submitWailsCompiledTurn(request, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const outcome = await bindings.CompanionService.SubmitCompiledTurn(request);
  return parseCompiledTurnOutcome(outcome);
}

/** Rust-parity entry: backend resolves available visual states from the conversation character. */
export async function submitWailsCompanionTurn(request, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const outcome = await bindings.CompanionService.SubmitTurn(request);
  return parseCompiledTurnOutcome(outcome);
}

export async function cancelWailsCompanionTurn(conversationId, turnId, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  await bindings.CompanionService.CancelTurn(conversationId, turnId);
}

export async function listenWailsHarnessEvents(onEvent, onProtocolError) {
  if (typeof onEvent !== "function" || typeof onProtocolError !== "function") {
    throw new TypeError("listenWailsHarnessEvents requires event and protocol error handlers");
  }
  const { Events } = await import("@wailsio/runtime");
  if (typeof Events?.On !== "function") {
    throw new Error("Wails Events.On is unavailable");
  }
  const off = await Events.On("companion-harness-event", (event) => {
    const payload = event?.data ?? event;
    try {
      onEvent(parseHarnessEvent(payload));
    } catch {
      onProtocolError(
        Object.freeze({
          code: "INVALID_HARNESS_EVENT",
          message: "收到无法识别的会话事件，已停止更新本轮回复。",
          retryable: false,
        }),
      );
    }
  });
  return normalizeWailsEventUnlisten(off);
}

export function normalizeWailsEventUnlisten(value) {
  if (typeof value === "function") return value;
  for (const key of ["off", "Off", "cancel", "Cancel", "dispose", "Dispose", "unsubscribe", "Unsubscribe"]) {
    if (typeof value?.[key] === "function") {
      return () => value[key]();
    }
  }
  return () => {};
}

export async function compactWailsConversation(conversationId, loadBindings = defaultLoadBindings) {
  const bindings = await loadBindings();
  const result = await bindings.CompanionService.CompactConversation(conversationId);
  return parseCompactionResult(result);
}

async function defaultLoadBindings() {
  const { loadFairyBindings } = await import("./wailsBindings.mjs");
  return loadFairyBindings();
}

function parseConversationBootstrap(value) {
  requireObject(value, "conversation bootstrap");
  rejectUnexpectedKeys(value, new Set(["conversation", "messages", "promptWindow"]), "conversation bootstrap");
  requireObject(value.conversation, "conversation bootstrap.conversation");
  rejectUnexpectedKeys(value.conversation, new Set(["id", "characterId", "createdAtUnixMs", "updatedAtUnixMs"]), "conversation bootstrap.conversation");
  const conversationId = requireString(value.conversation.id, "conversation.id");
  const conversation = Object.freeze({
    id: conversationId,
    characterId: requireString(value.conversation.characterId, "conversation.characterId"),
    createdAtUnixMs: requireNonNegativeInteger(value.conversation.createdAtUnixMs, "conversation.createdAtUnixMs"),
    updatedAtUnixMs: requireNonNegativeInteger(value.conversation.updatedAtUnixMs, "conversation.updatedAtUnixMs"),
  });
  const messages = Object.freeze(requireArray(value.messages, "messages").map((message, index) => parsePersistedMessage(message, index, conversationId)));
  for (let index = 1; index < messages.length; index += 1) {
    if (messages[index - 1].sequence >= messages[index].sequence) {
      throw new Error("conversation bootstrap messages must be strictly ordered");
    }
  }
  requireObject(value.promptWindow, "conversation bootstrap.promptWindow");
  rejectUnexpectedKeys(value.promptWindow, new Set(["conversationId", "revision", "summary", "cutoffMessageSequence", "updatedAtUnixMs"]), "conversation bootstrap.promptWindow");
  if (value.promptWindow.conversationId !== conversationId) {
    throw new Error("conversation bootstrap promptWindow conversationId must match");
  }
  const promptWindow = Object.freeze({
    conversationId,
    revision: requirePositiveInteger(value.promptWindow.revision, "promptWindow.revision"),
    summary: value.promptWindow.summary === null ? null : requireString(value.promptWindow.summary, "promptWindow.summary"),
    cutoffMessageSequence: requireNonNegativeInteger(value.promptWindow.cutoffMessageSequence, "promptWindow.cutoffMessageSequence"),
    updatedAtUnixMs: requireNonNegativeInteger(value.promptWindow.updatedAtUnixMs, "promptWindow.updatedAtUnixMs"),
  });
  return Object.freeze({ conversation, messages, promptWindow });
}

function parsePersistedMessage(value, index, conversationId) {
  requireObject(value, "conversation message");
  rejectUnexpectedKeys(value, new Set(["id", "conversationId", "turnId", "sequence", "role", "content", "createdAtUnixMs"]), "conversation message");
  if (value.conversationId !== conversationId) {
    throw new Error("conversation message conversationId must match");
  }
  if (value.role !== "user" && value.role !== "assistant") {
    throw new Error("conversation message role is unsupported");
  }
  return Object.freeze({
    id: requireString(value.id, `messages[${index}].id`),
    conversationId,
    turnId: requireString(value.turnId, `messages[${index}].turnId`),
    sequence: requirePositiveInteger(value.sequence, `messages[${index}].sequence`),
    role: value.role,
    content: requireString(value.content, `messages[${index}].content`),
    createdAtUnixMs: requireNonNegativeInteger(value.createdAtUnixMs, `messages[${index}].createdAtUnixMs`),
  });
}

export function parseBootstrapStatus(value) {
  if (!value || typeof value !== "object") {
    throw new Error("Wails bootstrap status must be an object");
  }
  const status = {
    appName: requireString(value.appName, "appName"),
    migrationStage: requireString(value.migrationStage, "migrationStage"),
    wailsVersion: requireString(value.wailsVersion, "wailsVersion"),
    respondRuntimeMigrated: requireBoolean(value.respondRuntimeMigrated, "respondRuntimeMigrated"),
  };
  return Object.freeze(status);
}

export function parseModelConnectionStatus(value) {
  requireObject(value, "Wails model connection status");
  rejectUnexpectedKeys(
    value,
    new Set([
      "configured",
      "protocol",
      "endpoint",
      "model",
      "contextWindowTokens",
      "authMode",
      "capabilities",
      "secretStorageMigrated",
    ]),
    "Wails model connection status",
  );
  rejectSecretFields(value, "Wails model connection status");

  const configured = requireBoolean(value.configured, "configured");
  const secretStorageMigrated = requireBoolean(value.secretStorageMigrated, "secretStorageMigrated");
  if (!configured) {
    rejectConfiguredFieldsOnUnconfiguredStatus(value);
    return Object.freeze({
      configured: false,
      secretStorageMigrated,
    });
  }

  const status = {
    configured: true,
    protocol: requireEnum(value.protocol, "protocol", new Set(["responses", "chat_completions"])),
    endpoint: requireString(value.endpoint, "endpoint"),
    model: requireString(value.model, "model"),
    contextWindowTokens: requirePositiveInteger(value.contextWindowTokens, "contextWindowTokens"),
    authMode: requireEnum(value.authMode, "authMode", new Set(["bearer_key", "no_auth"])),
    capabilities: parseGatewayCapabilities(value.capabilities),
    secretStorageMigrated,
  };
  return Object.freeze(status);
}

export function parseModelRequestDraft(value) {
  requireObject(value, "Wails model request draft");
  rejectUnexpectedKeys(
    value,
    new Set(["protocol", "method", "url", "contentType", "authRequirement", "bodyJSON"]),
    "Wails model request draft",
  );
  rejectSecretFields(value, "Wails model request draft");

  const draft = {
    protocol: requireEnum(value.protocol, "protocol", new Set(["responses", "chat_completions"])),
    method: requireEnum(value.method, "method", new Set(["POST"])),
    url: requireURL(value.url, "url"),
    contentType: requireEnum(value.contentType, "contentType", new Set(["application/json"])),
    authRequirement: requireEnum(
      value.authRequirement,
      "authRequirement",
      new Set(["bearer_key_required", "none"]),
    ),
    bodyJSON: requireString(value.bodyJSON, "bodyJSON"),
  };

  const body = parseDraftBodyJSON(draft.bodyJSON);
  rejectSecretFields(body, "Wails model request draft body");
  rejectBodySecretText(draft.bodyJSON);
  validateProtocolDraftBody(draft.protocol, body);

  return Object.freeze({
    ...draft,
    body: Object.freeze(body),
  });
}

export function parseMemorySummary(value) {
  requireObject(value, "Wails memory summary");
  rejectUnexpectedKeys(
    value,
    new Set([
      "schemaVersion",
      "conversations",
      "activeGlobalMemories",
      "activeCharacterMemories",
      "needsReviewMemories",
      "pendingExtractionTurns",
      "runningBatches",
      "failedBatches",
      "candidateKnowledge",
      "verifiedKnowledge",
      "readOnly",
    ]),
    "Wails memory summary",
  );
  return Object.freeze({
    schemaVersion: requirePositiveInteger(value.schemaVersion, "schemaVersion"),
    conversations: requireNonNegativeInteger(value.conversations, "conversations"),
    activeGlobalMemories: requireNonNegativeInteger(value.activeGlobalMemories, "activeGlobalMemories"),
    activeCharacterMemories: requireNonNegativeInteger(value.activeCharacterMemories, "activeCharacterMemories"),
    needsReviewMemories: requireNonNegativeInteger(value.needsReviewMemories, "needsReviewMemories"),
    pendingExtractionTurns: requireNonNegativeInteger(value.pendingExtractionTurns, "pendingExtractionTurns"),
    runningBatches: requireNonNegativeInteger(value.runningBatches, "runningBatches"),
    failedBatches: requireNonNegativeInteger(value.failedBatches, "failedBatches"),
    candidateKnowledge: requireNonNegativeInteger(value.candidateKnowledge, "candidateKnowledge"),
    verifiedKnowledge: requireNonNegativeInteger(value.verifiedKnowledge, "verifiedKnowledge"),
    readOnly: requireBoolean(value.readOnly, "readOnly"),
  });
}

export function parseCompiledTurnOutcome(value) {
  requireObject(value, "Wails compiled turn outcome");
  rejectUnexpectedKeys(
    value,
    new Set([
      "conversationId",
      "turnId",
      "responseText",
      "speechText",
      "speechRequested",
      "visualState",
      "chains",
      "respondMigrated",
      "migrationMessage",
    ]),
    "Wails compiled turn outcome",
  );
  rejectSecretFields(value, "Wails compiled turn outcome");
  const chains = parseReplyChains(value.chains);
  const outcome = {
    conversationId: requireString(value.conversationId, "conversationId"),
    turnId: requireString(value.turnId, "turnId"),
    responseText: requireString(value.responseText, "responseText"),
    speechText: requireString(value.speechText, "speechText"),
    sources: Object.freeze([]),
    usage: Object.freeze([]),
    characterRevision: 1,
    userProfileRevision: null,
    speechRequested: requireBoolean(value.speechRequested, "speechRequested"),
    visualState: requireString(value.visualState, "visualState"),
    chains,
    respondMigrated: requireBoolean(value.respondMigrated, "respondMigrated"),
  };
  if (outcome.responseText !== chains.map((chain) => chain.text).join("\n")) {
    throw new Error("Wails compiled turn outcome responseText must match chains");
  }
  if (outcome.speechText !== chains[0].speechText) {
    throw new Error("Wails compiled turn outcome speechText must match first chain");
  }
  if (outcome.visualState !== chains[chains.length - 1].visualState) {
    throw new Error("Wails compiled turn outcome visualState must match final chain");
  }
  return Object.freeze(outcome);
}

function parseReplyChains(value) {
  const chains = requireArray(value, "chains");
  if (chains.length === 0 || chains.length > 5) {
    throw new Error("Wails compiled turn outcome chains must contain 1-5 items");
  }
  return Object.freeze(chains.map(parseReplyChain));
}

function parseReplyChain(value) {
  requireObject(value, "reply chain");
  rejectUnexpectedKeys(value, new Set(["text", "speechText", "visualState"]), "reply chain");
  return Object.freeze({
    text: requireString(value.text, "chain.text"),
    speechText: requireString(value.speechText, "chain.speechText"),
    visualState: requireString(value.visualState, "chain.visualState"),
  });
}

function parseDraftBodyJSON(value) {
  let parsed;
  try {
    parsed = JSON.parse(value);
  } catch (error) {
    throw new Error("Wails model request draft bodyJSON must be valid JSON");
  }
  requireObject(parsed, "Wails model request draft bodyJSON");
  return parsed;
}

function rejectBodySecretText(value) {
  const lowered = value.toLowerCase();
  if (lowered.includes("authorization") || lowered.includes("bearer ") || value.includes("sk-")) {
    throw new Error("Wails model request draft bodyJSON must not expose secrets");
  }
}

function validateProtocolDraftBody(protocol, body) {
  if (protocol === "responses") {
    rejectUnexpectedKeys(
      body,
      new Set([
        "model",
        "instructions",
        "input",
        "previous_response_id",
        "max_output_tokens",
        "store",
        "stream",
        "text",
        "prompt_cache_key",
      ]),
      "Responses draft body",
    );
    requireString(body.model, "body.model");
    requireString(body.instructions, "body.instructions");
    requireArray(body.input, "body.input");
    requirePositiveInteger(body.max_output_tokens, "body.max_output_tokens");
    requireBoolean(body.store, "body.store");
    requireBoolean(body.stream, "body.stream");
    requireObject(body.text, "body.text");
    return;
  }

  rejectUnexpectedKeys(
    body,
    new Set(["model", "messages", "stream", "stream_options", "max_tokens", "response_format"]),
    "Chat Completions draft body",
  );
  requireString(body.model, "body.model");
  requireArray(body.messages, "body.messages");
  requireBoolean(body.stream, "body.stream");
  requireObject(body.stream_options, "body.stream_options");
  requireBoolean(body.stream_options.include_usage, "body.stream_options.include_usage");
  requirePositiveInteger(body.max_tokens, "body.max_tokens");
  if ("response_format" in body) {
    requireObject(body.response_format, "body.response_format");
    requireEnum(body.response_format.type, "body.response_format.type", new Set(["json_object"]));
  }
}

function parseGatewayCapabilities(value) {
  requireObject(value, "capabilities");
  rejectUnexpectedKeys(
    value,
    new Set([
      "promptCacheKey",
      "cachedTokensUsage",
      "explicitBreakpoints",
      "cacheRetention",
      "websocketContinuation",
    ]),
    "capabilities",
  );

  return Object.freeze({
    promptCacheKey: requireBoolean(value.promptCacheKey, "capabilities.promptCacheKey"),
    cachedTokensUsage: requireBoolean(value.cachedTokensUsage, "capabilities.cachedTokensUsage"),
    explicitBreakpoints: requireBoolean(value.explicitBreakpoints, "capabilities.explicitBreakpoints"),
    cacheRetention: requireBoolean(value.cacheRetention, "capabilities.cacheRetention"),
    websocketContinuation: requireBoolean(value.websocketContinuation, "capabilities.websocketContinuation"),
  });
}

function rejectConfiguredFieldsOnUnconfiguredStatus(value) {
  const stringFields = ["protocol", "endpoint", "model", "authMode"];
  for (const field of stringFields) {
    if (field in value && value[field] !== undefined && value[field] !== "") {
      throw new Error("Unconfigured Wails model status must not include " + field);
    }
  }
  if (
    "contextWindowTokens" in value &&
    value.contextWindowTokens !== undefined &&
    value.contextWindowTokens !== 0
  ) {
    throw new Error("Unconfigured Wails model status must not include contextWindowTokens");
  }
  if ("capabilities" in value && value.capabilities !== undefined) {
    requireObject(value.capabilities, "capabilities");
    for (const capability of Object.values(value.capabilities)) {
      if (capability !== false && capability !== undefined) {
        throw new Error("Unconfigured Wails model status must not include enabled capabilities");
      }
    }
  }
}

function rejectSecretFields(value, label) {
  const forbidden = new Set(["apiKey", "api_key", "authorization", "authorizationHeader", "bearerToken"]);
  for (const key of Object.keys(value)) {
    if (forbidden.has(key)) {
      throw new Error(label + " must not expose " + key);
    }
  }
}

function rejectUnexpectedKeys(value, allowed, label) {
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) {
      throw new Error(label + " contains unexpected field " + key);
    }
  }
}

function requireObject(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(label + " must be an object");
  }
}

function requireString(value, field) {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error("Wails status missing " + field);
  }
  return value;
}

function requireArray(value, field) {
  if (!Array.isArray(value)) {
    throw new Error("Wails status missing " + field);
  }
  return value;
}

function requireBoolean(value, field) {
  if (typeof value !== "boolean") {
    throw new Error("Wails status missing " + field);
  }
  return value;
}

function requirePositiveInteger(value, field) {
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new Error("Wails status missing " + field);
  }
  return value;
}

function requireNonNegativeInteger(value, field) {
  if (!Number.isSafeInteger(value) || value < 0) {
    throw new Error("Wails status missing " + field);
  }
  return value;
}

function requireEnum(value, field, allowed) {
  const text = requireString(value, field);
  if (!allowed.has(text)) {
    throw new Error("Wails status has unsupported " + field);
  }
  return text;
}

function requireURL(value, field) {
  const text = requireString(value, field);
  let parsed;
  try {
    parsed = new URL(text);
  } catch (error) {
    throw new Error("Wails status missing " + field);
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
    throw new Error("Wails status has unsupported " + field);
  }
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error("Wails status has unsupported " + field);
  }
  return text;
}
