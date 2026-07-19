import test from "node:test";
import assert from "node:assert/strict";

import {
  loadWailsBootstrap,
  loadWailsCharacterCatalog,
  loadWailsVisualPackCatalog,
  activateWailsCharacter,
  createWailsCharacter,
  createWailsCompanionSession,
  listenWailsHarnessEvents,
  normalizeWailsEventUnlisten,
  loadWailsActiveBackgroundJobs,
  loadWailsMemorySummary,
  loadWailsPersonalMemoryCatalog,
  loadWailsSemanticEmbeddingStatus,
  loadWailsModelRequestDraft,
  loadWailsModelStatus,
  loadWailsSpeechStatus,
  loadWailsUserProfile,
  parseBootstrapStatus,
  parseCompiledTurnOutcome,
  parseMemorySummary,
  parseModelConnectionStatus,
  parseModelRequestDraft,
  parseSemanticEmbeddingStatus,
  parseSpeechStatus,
  parseVoiceCloneResult,
  clearWailsModelConnection,
  clearWailsSpeechSettings,
  compactWailsConversation,
  clearWailsUserProfile,
  saveWailsModelConnection,
  saveWailsSpeechSettings,
  setWailsCharacterAppearance,
  setWailsUserProfile,
  submitWailsCompiledTurn,
  trainWailsVoice,
  queryWailsVoice,
  upgradeWailsVoice,
  updateWailsCharacter,
  assignWailsLegacyRelationship,
  confirmWailsKnowledgeCandidate,
  createWailsPersonalMemory,
  loadWailsExtractionBatchCatalog,
  loadWailsKnowledgeCatalog,
  reviseWailsPersonalMemory,
  retryWailsExtractionBatch,
  tombstoneWailsPersonalMemory,
  tombstoneWailsKnowledge,
} from "./wailsClient.mjs";

test("parseBootstrapStatus accepts explicit migration status", () => {
  assert.deepEqual(
    parseBootstrapStatus({
      appName: "FAIRY",
      migrationStage: "wails3-only",
      wailsVersion: "v3.0.0-alpha2.117",
      respondRuntimeMigrated: true,
    }),
    {
      appName: "FAIRY",
      migrationStage: "wails3-only",
      wailsVersion: "v3.0.0-alpha2.117",
      respondRuntimeMigrated: true,
    },
  );
});

test("parseBootstrapStatus rejects missing values instead of defaulting", () => {
  assert.throws(
    () =>
      parseBootstrapStatus({
        appName: "FAIRY",
        migrationStage: "wails3-only",
        wailsVersion: "v3.0.0-alpha2.117",
      }),
    /respondRuntimeMigrated/,
  );
});

test("parseBootstrapStatus ignores obsolete legacyTauriPreserved field", () => {
  assert.deepEqual(
    parseBootstrapStatus({
      appName: "FAIRY",
      migrationStage: "wails3-only",
      wailsVersion: "v3.0.0-alpha2.117",
      legacyTauriPreserved: true,
      respondRuntimeMigrated: true,
    }),
    {
      appName: "FAIRY",
      migrationStage: "wails3-only",
      wailsVersion: "v3.0.0-alpha2.117",
      respondRuntimeMigrated: true,
    },
  );
});

test("loadWailsBootstrap calls generated Wails binding loader", async () => {
  const status = await loadWailsBootstrap(async () => ({
    BootstrapService: {
      Status: async () => ({
        appName: "FAIRY",
        migrationStage: "wails3-only",
        wailsVersion: "v3.0.0-alpha2.117",
        respondRuntimeMigrated: true,
      }),
    },
  }));

  assert.equal(status.appName, "FAIRY");
  assert.equal(status.migrationStage, "wails3-only");
  assert.equal(status.respondRuntimeMigrated, true);
  assert.equal(Object.hasOwn(status, "legacyTauriPreserved"), false);
});

test("loadWailsCharacterCatalog calls generated CharacterService binding loader", async () => {
  const characterId = "6a129284-6358-47b0-ad64-2a5907d36c91";
  const catalog = await loadWailsCharacterCatalog(async () => ({
    CharacterService: {
      ListCharacters: async () => ({
        characters: [
          {
            characterId,
            revision: 1,
            name: "亚托莉",
            description: "认真听用户说话。",
            dialogueStyle: null,
            textLanguage: "zh",
    speakingLanguage: "ja",
            appearance: { status: "unassigned" },
          },
        ],
        active: null,
        diagnostics: [],
      }),
    },
  }));
  assert.equal(catalog.characters[0].characterId, characterId);
  assert.equal(catalog.characters[0].appearance.status, "unassigned");
});

test("loadWailsVisualPackCatalog calls generated VisualService binding loader", async () => {
  const catalog = await loadWailsVisualPackCatalog(async () => ({
    VisualService: {
      ListVisualPacks: async () => ({ visualPacks: [visualAssignedAppearance().visual] }),
    },
  }));
  assert.equal(catalog.visualPacks[0].packId, "fairy.atri");
});

function unassignedCharacter() {
  return {
    characterId: "6a129284-6358-47b0-ad64-2a5907d36c91",
    revision: 1,
    name: "亚托莉",
    description: "认真听用户说话。",
    dialogueStyle: null,
    textLanguage: "zh",
    speakingLanguage: "ja",
    appearance: { status: "unassigned" },
  };
}

function visualAssignedAppearance() {
  return {
    status: "assigned",
    bindingRevision: 1,
    visual: {
      schemaVersion: 2,
      packId: "fairy.atri",
      displayName: "Fairy",
      renderer: "state_images",
      frame: { width: 128, height: 128 },
      scale: 1,
      anchor: { x: 64, y: 127 },
      states: [
        {
          id: "idle",
          description: "idle 状态说明",
          imagePath: "fairy-character://localhost/fairy.atri/idle.png",
        },
      ],
    },
  };
}

test("Wails character mutation bindings parse strict character DTOs", async () => {
  const created = await createWailsCharacter(
    { name: "亚托莉", description: "认真听用户说话。" },
    "fairy.atri",
    async () => ({ CharacterService: { CreateCharacter: async () => unassignedCharacter() } }),
  );
  assert.equal(created.name, "亚托莉");

  const updated = await updateWailsCharacter(
    created.characterId,
    { name: "亚托莉", description: "会先听完再回应。" },
    async () => ({ CharacterService: { UpdateCharacter: async () => ({ ...unassignedCharacter(), revision: 2, description: "会先听完再回应。" }) } }),
  );
  assert.equal(updated.revision, 2);

  const assigned = await setWailsCharacterAppearance(
    created.characterId,
    "fairy.atri",
    async () => ({ CharacterService: { SetCharacterAppearance: async () => ({ ...unassignedCharacter(), appearance: visualAssignedAppearance() }) } }),
  );
  assert.equal(assigned.appearance.status, "assigned");

  const active = await activateWailsCharacter(
    created.characterId,
    2,
    async () => ({ CharacterService: { ActivateCharacter: async () => ({ ...unassignedCharacter(), revision: 2 }) } }),
  );
  assert.equal(active.revision, 2);
});

function configuredModelStatus() {
  return {
    configured: true,
    protocol: "chat_completions",
    endpoint: "https://api.deepseek.com",
    model: "deepseek-v4-flash",
    contextWindowTokens: 1048576,
    authMode: "bearer_key",
    capabilities: {
      promptCacheKey: false,
      cachedTokensUsage: true,
      explicitBreakpoints: false,
      cacheRetention: false,
      websocketContinuation: false,
    },
    secretStorageMigrated: true,
  };
}

function configuredSpeechStatus() {
  return {
    configured: true,
    enabled: true,
    baseUrl: "https://openspeech.bytedance.com",
    trainPath: "/api/v3/tts/voice_clone",
    queryPath: "/api/v3/tts/get_voice",
    upgradePath: "/upgrade_voice",
    appId: "9193177346",
    synthesisResourceId: "seed-icl-1.0",
    synthesisModel: "",
    defaultSpeaker: "S_voice",
    defaultLanguage: 0,
    defaultFormat: "wav",
    hasApiKey: true,
    hasAccessToken: false,
    secretMigrated: true,
  };
}

function voiceCloneResult() {
  return {
    httpStatus: 200,
    logid: "logid",
    speakerId: "S_voice",
    status: 2,
    availableTrainingTimes: 15,
    createTime: 1772026663000,
    language: 0,
    speakerStatus: [{ modelType: 5, demoAudio: "https://x.bytespeech.com/S_voice" }],
    code: "",
    message: "",
    rawJson: "{}",
  };
}

test("parseModelConnectionStatus accepts configured redacted model status", () => {
  assert.deepEqual(parseModelConnectionStatus(configuredModelStatus()), configuredModelStatus());
});

test("parseModelConnectionStatus accepts explicitly unconfigured status", () => {
  assert.deepEqual(
    parseModelConnectionStatus({
      configured: false,
      secretStorageMigrated: true,
    }),
    {
      configured: false,
      secretStorageMigrated: true,
    },
  );
});

test("parseModelConnectionStatus accepts generated zero-value fields when unconfigured", () => {
  assert.deepEqual(
    parseModelConnectionStatus({
      configured: false,
      protocol: "",
      endpoint: "",
      model: "",
      contextWindowTokens: 0,
      authMode: "",
      capabilities: {
        promptCacheKey: false,
        cachedTokensUsage: false,
        explicitBreakpoints: false,
        cacheRetention: false,
        websocketContinuation: false,
      },
      secretStorageMigrated: true,
    }),
    {
      configured: false,
      secretStorageMigrated: true,
    },
  );
});

test("parseModelConnectionStatus rejects missing configured fields instead of defaulting", () => {
  const status = configuredModelStatus();
  delete status.endpoint;
  assert.throws(() => parseModelConnectionStatus(status), /endpoint/);
});

test("parseModelConnectionStatus rejects leaked secret fields", () => {
  assert.throws(
    () =>
      parseModelConnectionStatus({
        ...configuredModelStatus(),
        apiKey: "sk-not-allowed",
      }),
    /unexpected field apiKey/,
  );
});

test("parseModelConnectionStatus rejects unsupported protocol", () => {
  assert.throws(
    () =>
      parseModelConnectionStatus({
        ...configuredModelStatus(),
        protocol: "auto",
      }),
    /unsupported protocol/,
  );
});

test("loadWailsModelStatus calls generated ConfigService binding loader", async () => {
  const status = await loadWailsModelStatus(async () => ({
    ConfigService: {
      ModelStatus: async () => configuredModelStatus(),
    },
  }));

  assert.equal(status.configured, true);
  assert.equal(status.endpoint, "https://api.deepseek.com");
  assert.equal(status.capabilities.cachedTokensUsage, true);
  assert.equal(status.secretStorageMigrated, true);
});

test("saveWailsModelConnection calls generated ConfigService binding loader", async () => {
  const status = await saveWailsModelConnection(
    {
      protocol: "chat_completions",
      endpoint: "https://api.deepseek.com",
      model: "deepseek-v4-flash",
      contextWindowTokens: 1048576,
      authMode: "bearer_key",
    },
    "sk-test-secret",
    async () => ({
      ConfigService: {
        SaveModelConnection: async (input, apiKey) => {
          assert.equal(input.protocol, "chat_completions");
          assert.equal(apiKey, "sk-test-secret");
          return configuredModelStatus();
        },
      },
    }),
  );
  assert.equal(status.configured, true);
});

test("clearWailsModelConnection calls generated ConfigService binding loader", async () => {
  const status = await clearWailsModelConnection(async () => ({
    ConfigService: {
      ClearModelConnection: async () => ({ configured: false, secretStorageMigrated: true }),
    },
  }));
  assert.equal(status.configured, false);
});

test("parseSpeechStatus accepts redacted configured status and rejects leaked token", () => {
  assert.deepEqual(parseSpeechStatus(configuredSpeechStatus()), configuredSpeechStatus());
  assert.throws(
    () => parseSpeechStatus({ ...configuredSpeechStatus(), accessToken: "not-allowed" }),
    /accessToken/,
  );
  assert.throws(
    () => parseSpeechStatus({ ...configuredSpeechStatus(), apiKey: "not-allowed" }),
    /apiKey/,
  );
  assert.throws(
    () => parseSpeechStatus({ ...configuredSpeechStatus(), baseUrl: "wss://example.com" }),
    /baseUrl/,
  );
});

test("parseVoiceCloneResult accepts structured provider fields", () => {
  assert.deepEqual(parseVoiceCloneResult(voiceCloneResult()), voiceCloneResult());
  assert.throws(
    () => parseVoiceCloneResult({ ...voiceCloneResult(), accessToken: "not-allowed" }),
    /accessToken/,
  );
});

test("Wails speech bindings parse status, settings, clear, train, query, and upgrade", async () => {
  const status = await loadWailsSpeechStatus(async () => ({
    SpeechService: { Status: async () => configuredSpeechStatus() },
  }));
  assert.equal(status.configured, true);

  const saved = await saveWailsSpeechSettings(
    { enabled: true, apiKey: "secret" },
    async () => ({
      SpeechService: {
        SaveSettings: async (input) => {
          assert.equal(input.apiKey, "secret");
          return configuredSpeechStatus();
        },
      },
    }),
  );
  assert.equal(saved.hasApiKey, true);

  const cleared = await clearWailsSpeechSettings(async () => ({
    SpeechService: {
      ClearSettings: async () => ({ ...configuredSpeechStatus(), configured: false, enabled: false, appId: "", hasApiKey: false, hasAccessToken: false }),
    },
  }));
  assert.equal(cleared.configured, false);

  const trained = await trainWailsVoice({ speakerId: "S_voice", audioData: "ZmFrZQ==", audioFormat: "wav", language: 0 }, async () => ({
    SpeechService: {
      TrainVoice: async (request) => {
        assert.deepEqual(request, { speakerId: "S_voice", audioData: "ZmFrZQ==", audioFormat: "wav", language: 0 });
        return voiceCloneResult();
      },
    },
  }));
  assert.equal(trained.speakerId, "S_voice");

  const queried = await queryWailsVoice({ speakerId: "S_voice" }, async () => ({
    SpeechService: {
      QueryVoice: async (request) => {
        assert.deepEqual(request, { speakerId: "S_voice" });
        return voiceCloneResult();
      },
    },
  }));
  assert.equal(queried.status, 2);

  const upgraded = await upgradeWailsVoice({ speakerId: "S_voice" }, async () => ({
    SpeechService: {
      UpgradeVoice: async (request) => {
        assert.deepEqual(request, { speakerId: "S_voice" });
        return voiceCloneResult();
      },
    },
  }));
  assert.equal(upgraded.availableTrainingTimes, 15);
});

test("Wails profile bindings parse current set and clear", async () => {
  const profile = await loadWailsUserProfile(async () => ({
    ProfileService: {
      Current: async () => ({ revision: 1, preferredName: "Rinai" }),
    },
  }));
  assert.equal(profile.preferredName, "Rinai");

  const setUpdate = await setWailsUserProfile("凛", async () => ({
    ProfileService: {
      SetPreferredName: async (preferredName) => ({
        profile: { revision: 2, preferredName },
        changed: true,
        recoveredCorruption: false,
      }),
    },
  }));
  assert.equal(setUpdate.profile.preferredName, "凛");

  const clearUpdate = await clearWailsUserProfile(async () => ({
    ProfileService: {
      Clear: async () => ({
        profile: { revision: 3, preferredName: null },
        changed: true,
        recoveredCorruption: false,
      }),
    },
  }));
  assert.equal(clearUpdate.profile.preferredName, null);
});

function chatDraft() {
  return {
    protocol: "chat_completions",
    method: "POST",
    url: "https://api.deepseek.com/chat/completions",
    contentType: "application/json",
    authRequirement: "bearer_key_required",
    bodyJSON: JSON.stringify({
      model: "deepseek-v4-flash",
      messages: [
        { role: "system", content: "stable instructions" },
        { role: "user", content: "你好" },
      ],
      stream: true,
      stream_options: { include_usage: true },
      max_tokens: 160,
      response_format: { type: "json_object" },
    }),
  };
}

function responsesDraft() {
  return {
    protocol: "responses",
    method: "POST",
    url: "https://api.deepseek.com/responses",
    contentType: "application/json",
    authRequirement: "none",
    bodyJSON: JSON.stringify({
      model: "deepseek-v4-flash",
      instructions: "stable instructions",
      input: [{ role: "user", content: "你好" }],
      max_output_tokens: 160,
      store: false,
      stream: true,
      text: { format: { type: "text" } },
      prompt_cache_key: "fairy:conversation:respond",
    }),
  };
}

test("parseModelRequestDraft accepts Chat Completions request draft", () => {
  const draft = parseModelRequestDraft(chatDraft());
  assert.equal(draft.protocol, "chat_completions");
  assert.equal(draft.method, "POST");
  assert.equal(draft.authRequirement, "bearer_key_required");
  assert.equal(draft.body.response_format.type, "json_object");
  assert.equal(draft.body.stream_options.include_usage, true);
});

test("parseModelRequestDraft accepts Responses request draft", () => {
  const draft = parseModelRequestDraft(responsesDraft());
  assert.equal(draft.protocol, "responses");
  assert.equal(draft.authRequirement, "none");
  assert.equal(draft.body.store, false);
  assert.equal(draft.body.prompt_cache_key, "fairy:conversation:respond");
});

test("parseModelRequestDraft rejects secret-shaped body content", () => {
  assert.throws(
    () =>
      parseModelRequestDraft({
        ...chatDraft(),
        bodyJSON: JSON.stringify({
          model: "deepseek-v4-flash",
          messages: [],
          stream: true,
          stream_options: { include_usage: true },
          max_tokens: 160,
          authorization: "Bearer sk-nope",
        }),
      }),
    /authorization|must not expose secrets/,
  );
});

test("parseModelRequestDraft rejects unsupported protocol body shape", () => {
  assert.throws(
    () =>
      parseModelRequestDraft({
        ...chatDraft(),
        bodyJSON: JSON.stringify({
          model: "deepseek-v4-flash",
          messages: [],
          stream: true,
          stream_options: { include_usage: true },
          max_tokens: 160,
          tools: [],
        }),
      }),
    /tools/,
  );
});

test("loadWailsModelRequestDraft calls generated ModelService binding loader", async () => {
  const draft = await loadWailsModelRequestDraft(
    {
      shape: {
        lane: "respond",
        model: "deepseek-v4-flash",
        instructions: "stable instructions",
        maxOutputTokens: 160,
      },
      input: [{ type: "user_message", content: "你好" }],
    },
    async () => ({
      ModelService: {
        BuildRequestDraft: async () => chatDraft(),
      },
    }),
  );

  assert.equal(draft.protocol, "chat_completions");
  assert.equal(draft.body.messages[1].content, "你好");
});

function memorySummary() {
  return {
    schemaVersion: 3,
    conversations: 1,
    activeGlobalMemories: 2,
    activeCharacterMemories: 3,
    needsReviewMemories: 0,
    pendingExtractionTurns: 1,
    runningBatches: 0,
    failedBatches: 1,
    candidateKnowledge: 2,
    verifiedKnowledge: 4,
    readOnly: true,
  };
}

test("parseMemorySummary accepts explicit read-only memory counts", () => {
  assert.deepEqual(parseMemorySummary(memorySummary()), memorySummary());
});

test("parseMemorySummary rejects negative counts", () => {
  assert.throws(
    () => parseMemorySummary({ ...memorySummary(), failedBatches: -1 }),
    /failedBatches/,
  );
});

test("loadWailsMemorySummary calls generated MemoryService binding loader", async () => {
  const summary = await loadWailsMemorySummary(async () => ({
    MemoryService: {
      Summary: async () => memorySummary(),
    },
  }));
  assert.equal(summary.schemaVersion, 3);
  assert.equal(summary.readOnly, true);
});

function semanticEmbeddingStatus(overrides = {}) {
  return {
    modelId: "bge-small-zh-v1.5",
    dimensions: 512,
    modelPath: "/tmp/fairy/intelligence/embeddings/bge-small-zh-v1.5/model.onnx",
    modelStatus: "missing",
    runtimeStatus: "unavailable",
    databaseStatus: "ready",
    semanticStatus: "unavailable",
    reason: "model_missing",
    pendingJobs: 2,
    runningJobs: 0,
    failedJobs: 1,
    embeddedItems: 3,
    vectorRows: 3,
    ...overrides,
  };
}

test("parseSemanticEmbeddingStatus accepts explicit readiness and queue counts", () => {
  const status = semanticEmbeddingStatus();
  assert.deepEqual(parseSemanticEmbeddingStatus(status), status);
  assert.throws(
    () => parseSemanticEmbeddingStatus({ ...status, pendingJobs: -1 }),
    /pendingJobs/,
  );
  assert.throws(
    () => parseSemanticEmbeddingStatus({ ...status, apiKey: "secret" }),
    /apiKey/,
  );
});

test("loadWailsSemanticEmbeddingStatus calls generated MemoryService binding loader", async () => {
  const status = await loadWailsSemanticEmbeddingStatus(async () => ({
    MemoryService: {
      SemanticEmbeddingStatus: async () => semanticEmbeddingStatus({ pendingJobs: 4 }),
    },
  }));
  assert.equal(status.modelId, "bge-small-zh-v1.5");
  assert.equal(status.pendingJobs, 4);
});

test("loadWailsActiveBackgroundJobs calls CompanionService binding", async () => {
  const jobs = await loadWailsActiveBackgroundJobs(async () => ({
    CompanionService: {
      ActiveBackgroundJobs: async () => 2,
    },
  }));
  assert.equal(jobs, 2);
});

function memoryRecord(overrides = {}) {
  return {
    id: "6a129284-6358-47b0-ad64-2a5907d36c91",
    kind: "preference",
    scope: { type: "global" },
    reviewStatus: "ready",
    content: "喜欢安静",
    status: "active",
    confidenceBasisPoints: 9000,
    sourceConversationId: "6a129284-6358-47b0-ad64-2a5907d36c92",
    sourceTurnId: "6a129284-6358-47b0-ad64-2a5907d36c93",
    supersedesId: null,
    createdAtUnixMs: 1,
    updatedAtUnixMs: 1,
    ...overrides,
  };
}

test("Wails personal memory bindings parse catalog and mutations", async () => {
  const catalog = await loadWailsPersonalMemoryCatalog("6a129284-6358-47b0-ad64-2a5907d36c91", async () => ({
    MemoryService: { PersonalMemoryCatalog: async () => ({ global: [memoryRecord()], character: [], needsReview: [] }) },
  }));
  assert.equal(catalog.global[0].content, "喜欢安静");

  const created = await createWailsPersonalMemory(
    { kind: "preference", scope: { type: "global" }, content: "喜欢安静" },
    async () => ({ MemoryService: { CreatePersonalMemory: async () => memoryRecord() } }),
  );
  assert.equal(created.kind, "preference");

  const revised = await reviseWailsPersonalMemory(
    created.id,
    "喜欢更安静",
    9000,
    async () => ({ MemoryService: { RevisePersonalMemory: async () => memoryRecord({ content: "喜欢更安静", supersedesId: created.id }) } }),
  );
  assert.equal(revised.supersedesId, created.id);

  await tombstoneWailsPersonalMemory(created.id, async () => ({ MemoryService: { TombstonePersonalMemory: async () => undefined } }));

  const assigned = await assignWailsLegacyRelationship(
    created.id,
    "6a129284-6358-47b0-ad64-2a5907d36c91",
    async () => ({ MemoryService: { AssignLegacyRelationship: async () => memoryRecord({ kind: "relationship", scope: { type: "character", characterId: "6a129284-6358-47b0-ad64-2a5907d36c91" } }) } }),
  );
  assert.equal(assigned.scope.type, "character");
});

function knowledgeRecord(overrides = {}) {
  return {
    id: "6a129284-6358-47b0-ad64-2a5907d36c94",
    topic: "主题",
    statement: "陈述",
    status: "candidate",
    verificationBasis: "unverified",
    confidenceBasisPoints: 8000,
    sourceConversationId: "6a129284-6358-47b0-ad64-2a5907d36c92",
    sourceTurnId: "6a129284-6358-47b0-ad64-2a5907d36c93",
    supersedesId: null,
    sources: [],
    createdAtUnixMs: 1,
    updatedAtUnixMs: 1,
    ...overrides,
  };
}

function extractionBatch(overrides = {}) {
  return {
    id: "6a129284-6358-47b0-ad64-2a5907d36c95",
    conversationId: "6a129284-6358-47b0-ad64-2a5907d36c92",
    characterId: "6a129284-6358-47b0-ad64-2a5907d36c91",
    status: "failed",
    firstTurnSequence: 1,
    lastTurnSequence: 1,
    error: { code: "MODEL_FAILED", message: "模型失败", retryable: true },
    createdAtUnixMs: 1,
    updatedAtUnixMs: 1,
    ...overrides,
  };
}

test("Wails knowledge and extraction bindings parse catalogs and mutations", async () => {
  const catalog = await loadWailsKnowledgeCatalog(async () => ({
    MemoryService: { KnowledgeCatalog: async () => ({ candidates: [knowledgeRecord()], verified: [] }) },
  }));
  assert.equal(catalog.candidates[0].topic, "主题");

  const confirmed = await confirmWailsKnowledgeCandidate("6a129284-6358-47b0-ad64-2a5907d36c94", async () => ({
    MemoryService: { ConfirmKnowledgeCandidate: async () => knowledgeRecord({ status: "verified", verificationBasis: "user_confirmed" }) },
  }));
  assert.equal(confirmed.status, "verified");

  await tombstoneWailsKnowledge("6a129284-6358-47b0-ad64-2a5907d36c94", async () => ({
    MemoryService: { TombstoneKnowledge: async () => undefined },
  }));

  const batches = await loadWailsExtractionBatchCatalog("6a129284-6358-47b0-ad64-2a5907d36c91", async () => ({
    MemoryService: { ExtractionBatchCatalog: async () => ({ running: [], failed: [extractionBatch()] }) },
  }));
  assert.equal(batches.failed[0].error.retryable, true);

  await retryWailsExtractionBatch("6a129284-6358-47b0-ad64-2a5907d36c95", async () => ({
    MemoryService: { RetryExtractionBatch: async () => undefined },
  }));
});

function conversationBootstrap() {
  return {
    conversation: {
      id: "conversation-1",
      characterId: "character-1",
      createdAtUnixMs: 1,
      updatedAtUnixMs: 1,
    },
    messages: [
      {
        id: "message-1",
        conversationId: "conversation-1",
        turnId: "turn-1",
        sequence: 1,
        role: "user",
        content: "你好",
        createdAtUnixMs: 1,
      },
    ],
    promptWindow: {
      conversationId: "conversation-1",
      revision: 1,
      summary: null,
      cutoffMessageSequence: 0,
      updatedAtUnixMs: 1,
    },
  };
}

test("createWailsCompanionSession calls MemoryService bootstrap binding", async () => {
  const session = await createWailsCompanionSession("character-1", async () => ({
    MemoryService: {
      OpenOrCreateCharacterConversation: async (characterId) => {
        assert.equal(characterId, "character-1");
        return conversationBootstrap();
      },
    },
  }));
  assert.equal(session.conversation.id, "conversation-1");
  assert.equal(session.messages[0].content, "你好");
});

function compiledTurnOutcome() {
  return {
    conversationId: "conversation-1",
    turnId: "turn-1",
    responseText: "我在。",
    speechText: "我在。",
    speechRequested: true,
    visualState: "happy",
    chains: [{ text: "我在。", speechText: "我在。", visualState: "happy" }],
    respondMigrated: true,
    migrationMessage: "",
  };
}

test("parseCompiledTurnOutcome accepts strict reply chains", () => {
  const outcome = parseCompiledTurnOutcome(compiledTurnOutcome());
  assert.equal(outcome.responseText, "我在。");
  assert.equal(outcome.chains[0].visualState, "happy");
  assert.equal(outcome.respondMigrated, true);
});

test("parseCompiledTurnOutcome accepts joined multi-chain speechText", () => {
  const outcome = parseCompiledTurnOutcome({
    conversationId: "conversation-1",
    turnId: "turn-1",
    responseText: "嗯，我懂。\n先这样改。",
    speechText: "うん、わかった。 まずこう直そう。",
    speechRequested: true,
    visualState: "happy",
    chains: [
      { text: "嗯，我懂。", speechText: "うん、わかった。", visualState: "thinking" },
      { text: "先这样改。", speechText: "まずこう直そう。", visualState: "happy" },
    ],
    respondMigrated: true,
    migrationMessage: "",
  });
  assert.equal(outcome.speechText, "うん、わかった。 まずこう直そう。");
  assert.equal(outcome.visualState, "happy");
});

test("parseCompiledTurnOutcome accepts empty speechText when TTS is skipped", () => {
  const outcome = parseCompiledTurnOutcome({
    conversationId: "conversation-1",
    turnId: "turn-1",
    responseText: "我在。",
    speechText: "",
    speechRequested: true,
    visualState: "idle",
    chains: [{ text: "我在。", speechText: "", visualState: "idle" }],
    respondMigrated: true,
    migrationMessage: "",
  });
  assert.equal(outcome.speechText, "");
  assert.equal(outcome.chains[0].speechText, "");
});

test("parseCompiledTurnOutcome rejects inconsistent aggregate fields", () => {
  assert.throws(
    () => parseCompiledTurnOutcome({ ...compiledTurnOutcome(), responseText: "不一致" }),
    /responseText must match chains/,
  );
  assert.throws(
    () => parseCompiledTurnOutcome({ ...compiledTurnOutcome(), speechText: "不一致" }),
    /speechText must match chains/,
  );
  assert.throws(
    () => parseCompiledTurnOutcome({ ...compiledTurnOutcome(), visualState: "idle" }),
    /visualState must match final chain/,
  );
});

test("parseCompiledTurnOutcome rejects leaked secret fields", () => {
  assert.throws(
    () => parseCompiledTurnOutcome({ ...compiledTurnOutcome(), apiKey: "sk-nope" }),
    /apiKey/,
  );
});

test("submitWailsCompiledTurn calls generated CompanionService binding loader", async () => {
  const outcome = await submitWailsCompiledTurn(
    {
      conversationId: "conversation-1",
      input: "你好",
      speechEnabled: true,
      maxOutputTokens: 160,
      availableVisualStates: [{ id: "idle", description: "idle 状态说明" }],
    },
    async () => ({
      CompanionService: {
        SubmitCompiledTurn: async () => compiledTurnOutcome(),
      },
    }),
  );
  assert.equal(outcome.turnId, "turn-1");
  assert.equal(outcome.speechRequested, true);
});

test("compactWailsConversation calls generated CompanionService binding loader", async () => {
  const result = await compactWailsConversation("6a129284-6358-47b0-ad64-2a5907d36c92", async () => ({
    CompanionService: {
      CompactConversation: async () => ({ windowRevision: 2, retainedDialogueItems: 0 }),
    },
  }));
  assert.equal(result.windowRevision, 2);
});

test("listenWailsHarnessEvents requires both event and protocol error handlers", async () => {
  await assert.rejects(
    () => listenWailsHarnessEvents(() => {}),
    /protocol error handlers/,
  );
  await assert.rejects(
    () => listenWailsHarnessEvents(null, () => {}),
    /protocol error handlers/,
  );
});

test("normalizeWailsEventUnlisten accepts Wails v3 subscription shapes", () => {
  let calls = 0;
  normalizeWailsEventUnlisten(() => { calls += 1; })();
  assert.equal(calls, 1);

  const subscription = { Cancel: () => { calls += 1; } };
  normalizeWailsEventUnlisten(subscription)();
  assert.equal(calls, 2);

  normalizeWailsEventUnlisten(null)();
  assert.equal(calls, 2);
});
