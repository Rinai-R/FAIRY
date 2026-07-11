import { Channel, invoke } from "@tauri-apps/api/core";

import {
  normalizeCompanionError,
  parseCharacter,
  parseCharacterActivation,
  parseCharacterCatalog,
  parseCompactionResult,
  parseConversationBootstrap,
  parseHarnessEvent,
  parseIntelligenceStatus,
  parseExtractionBatchCatalog,
  parseKnowledgeCatalog,
  parseConfirmedKnowledgeRecord,
  parseModelConnectionStatus,
  parsePersonalMemory,
  parsePersonalMemoryCatalog,
  parseSessionSnapshot,
  parseTurnOutcome,
  parseUserProfile,
  parseUserProfileUpdate,
} from "./companionState.mjs";

async function invokeParsed(command, args, parser) {
  try {
    return parser(await invoke(command, args));
  } catch (error) {
    throw normalizeCompanionError(error);
  }
}

export function createCompanionSession() {
  return invokeParsed(
    "create_companion_session",
    undefined,
    parseConversationBootstrap,
  );
}

export function getCompanionSession(conversationId) {
  return invokeParsed(
    "get_companion_session",
    { conversationId },
    parseSessionSnapshot,
  );
}

export async function submitCompanionTurn({
  conversationId,
  input,
  speechEnabled = false,
  onEvent,
  onProtocolError,
}) {
  if (typeof input !== "string" || input.trim().length === 0) {
    throw Object.freeze({
      code: "EMPTY_TURN_INPUT",
      message: "请输入想和角色说的话。",
      retryable: false,
    });
  }
  if (typeof onEvent !== "function" || typeof onProtocolError !== "function") {
    throw new TypeError("turn event and protocol error handlers are required");
  }

  const channel = new Channel();
  channel.onmessage = (value) => {
    try {
      onEvent(parseHarnessEvent(value));
    } catch {
      onProtocolError(
        Object.freeze({
          code: "INVALID_HARNESS_EVENT",
          message: "收到无法识别的会话事件，已停止更新本轮回复。",
          retryable: false,
        }),
      );
    }
  };

  return invokeParsed(
    "submit_companion_turn",
    {
      conversationId,
      input: input.trim(),
      speechEnabled,
      onEvent: channel,
    },
    parseTurnOutcome,
  );
}

export async function cancelCompanionTurn(turnId) {
  try {
    await invoke("cancel_companion_turn", { turnId });
  } catch (error) {
    throw normalizeCompanionError(error);
  }
}

export function compactCompanionSession(conversationId) {
  return invokeParsed(
    "compact_companion_session",
    { conversationId },
    parseCompactionResult,
  );
}

export function createCharacter(brief) {
  return invokeParsed("create_character", { brief }, parseCharacter);
}

export function updateCharacter(characterId, brief) {
  return invokeParsed(
    "update_character",
    { characterId, brief },
    parseCharacter,
  );
}

export function listCharacters() {
  return invokeParsed("list_characters", undefined, parseCharacterCatalog);
}

export function activateCharacter(characterId, revision) {
  return invokeParsed(
    "activate_character",
    { characterId, revision },
    parseCharacterActivation,
  );
}

export function getUserProfile() {
  return invokeParsed("get_user_profile", undefined, parseUserProfile);
}

export function setUserProfile(preferredName, conversationId = null) {
  return invokeParsed(
    "set_user_profile",
    { input: { preferredName }, conversationId },
    parseUserProfileUpdate,
  );
}

export function clearUserProfile(conversationId = null) {
  return invokeParsed(
    "clear_user_profile",
    { conversationId },
    parseUserProfileUpdate,
  );
}

export function getModelConnectionStatus() {
  return invokeParsed(
    "get_model_connection_status",
    undefined,
    parseModelConnectionStatus,
  );
}

export function saveModelConnection(input, apiKey = null) {
  return invokeParsed(
    "save_model_connection",
    { input, apiKey },
    parseModelConnectionStatus,
  );
}

export function clearModelConnection() {
  return invokeParsed(
    "clear_model_connection",
    undefined,
    parseModelConnectionStatus,
  );
}

export function getIntelligenceStatus() {
  return invokeParsed(
    "get_intelligence_status",
    undefined,
    parseIntelligenceStatus,
  );
}

export function getKnowledgeCatalog() {
  return invokeParsed(
    "get_knowledge_catalog",
    undefined,
    parseKnowledgeCatalog,
  );
}

export function confirmKnowledgeCandidate(id) {
  return invokeParsed(
    "confirm_knowledge_candidate",
    { id },
    parseConfirmedKnowledgeRecord,
  );
}

export async function tombstoneKnowledge(id) {
  try {
    const result = await invoke("tombstone_knowledge", { id });
    if (result !== null) {
      throw new TypeError("tombstone_knowledge must return null");
    }
  } catch (error) {
    throw normalizeCompanionError(error);
  }
}

export function getPersonalMemoryCatalog(characterId) {
  return invokeParsed(
    "get_personal_memory_catalog",
    { characterId },
    parsePersonalMemoryCatalog,
  );
}

export function getExtractionBatchCatalog(characterId) {
  return invokeParsed(
    "get_extraction_batch_catalog",
    { characterId },
    parseExtractionBatchCatalog,
  );
}

export function createPersonalMemory({ kind, scope, content, confidenceBasisPoints = 9000 }) {
  return invokeParsed(
    "create_personal_memory",
    { kind, scope, content, confidenceBasisPoints },
    parsePersonalMemory,
  );
}

export function revisePersonalMemory(id, content, confidenceBasisPoints = 9000) {
  return invokeParsed(
    "revise_personal_memory",
    { id, content, confidenceBasisPoints },
    parsePersonalMemory,
  );
}

async function invokeNull(command, args) {
  try {
    const result = await invoke(command, args);
    if (result !== null) throw new TypeError(`${command} must return null`);
  } catch (error) {
    throw normalizeCompanionError(error);
  }
}

export function tombstonePersonalMemory(id) {
  return invokeNull("tombstone_personal_memory", { id });
}

export function assignLegacyRelationship(id, characterId) {
  return invokeParsed(
    "assign_legacy_relationship",
    { id, characterId },
    parsePersonalMemory,
  );
}

export function retryExtractionBatch(id) {
  return invokeNull("retry_extraction_batch", { id });
}
