const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const TURN_STATES = new Set([
  "idle",
  "interpreting",
  "planning",
  "responding",
  "completed",
  "interrupted",
  "failed",
]);
const LANES = new Set(["respond", "compact", "extract"]);

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
        ],
        "event.payload",
      );
      return Object.freeze({
        type: value.type,
        text: parseNonEmptyString(value.text, "event.payload.text"),
        speechText: parseNonEmptyString(
          value.speechText,
          "event.payload.speechText",
        ),
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
    speechText: parseNonEmptyString(
      value.speechText,
      "turn outcome.speechText",
    ),
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
    ["characterId", "revision", "name", "description"],
    "character",
  );
  return Object.freeze({
    characterId: parseUuid(value.characterId, "character.characterId"),
    revision: parseRevision(value.revision, "character.revision"),
    name: parseNonEmptyString(value.name, "character.name"),
    description: parseNonEmptyString(value.description, "character.description"),
  });
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
      ["protocol", "endpoint", "model", "authMode"],
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

export function parseSearchConnectionStatus(value) {
  assertExactKeys(
    value,
    ["configured", "ready", "config", "error"],
    "search status",
  );
  if (typeof value.configured !== "boolean" || typeof value.ready !== "boolean") {
    throw new TypeError("search status flags must be booleans");
  }
  let config = null;
  if (value.config !== null) {
    assertExactKeys(value.config, ["provider", "endpoint"], "search status.config");
    if (value.config.provider !== "brave") {
      throw new TypeError("search status.config provider is unsupported");
    }
    config = Object.freeze({
      provider: value.config.provider,
      endpoint: parseNonEmptyString(
        value.config.endpoint,
        "search status.config.endpoint",
      ),
    });
  }
  return Object.freeze({
    configured: value.configured,
    ready: value.ready,
    config,
    error:
      value.error === null
        ? null
        : parseWireError(value.error, "search status.error"),
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
        "activePersonalMemories",
        "candidateKnowledge",
        "verifiedKnowledge",
        "pendingJobs",
        "runningJobs",
        "failedJobs",
      ],
      "intelligence status.summary",
    );
    summary = Object.freeze({
      activePersonalMemories: parseNonNegativeInteger(
        value.summary.activePersonalMemories,
        "intelligence status.summary.activePersonalMemories",
      ),
      candidateKnowledge: parseNonNegativeInteger(
        value.summary.candidateKnowledge,
        "intelligence status.summary.candidateKnowledge",
      ),
      verifiedKnowledge: parseNonNegativeInteger(
        value.summary.verifiedKnowledge,
        "intelligence status.summary.verifiedKnowledge",
      ),
      pendingJobs: parseNonNegativeInteger(
        value.summary.pendingJobs,
        "intelligence status.summary.pendingJobs",
      ),
      runningJobs: parseNonNegativeInteger(
        value.summary.runningJobs,
        "intelligence status.summary.runningJobs",
      ),
      failedJobs: parseNonNegativeInteger(
        value.summary.failedJobs,
        "intelligence status.summary.failedJobs",
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

export function createCompanionState() {
  return Object.freeze({
    conversationId: null,
    sessionState: "idle",
    activeTurnId: null,
    terminalTurn: null,
    lastSequence: 0,
    draft: "",
    responseDraft: "",
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

function reduceHarnessEvent(state, event) {
  if (state.conversationId !== event.conversationId) {
    protocolError("conversation id does not match the active session");
  }
  if (event.sequence !== state.lastSequence + 1) {
    protocolError("event sequence is duplicated or out of order");
  }

  if (state.terminalTurn !== null) {
    const speech = event.payload.type === "speech.requested";
    if (
      !speech ||
      state.terminalTurn.state !== "completed" ||
      state.speechRequest !== null ||
      event.turnId !== state.terminalTurn.turnId ||
      event.state !== "completed"
    ) {
      protocolError("terminal turn cannot accept this event");
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

  const activeTurnId = state.activeTurnId ?? event.turnId;
  if (event.turnId !== activeTurnId) {
    protocolError("turn id changed before a terminal event");
  }

  if (event.payload.type === "state_changed") {
    if (event.state === "completed" || event.state === "failed") {
      protocolError("completed and failed states require typed terminal payloads");
    }
    if (event.state === "interrupted") {
      if (!["interpreting", "planning", "responding"].includes(state.sessionState)) {
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
      interpreting: "planning",
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
        characterRevision: event.payload.characterRevision,
        userProfileRevision: event.payload.userProfileRevision,
      }),
      lastSequence: event.sequence,
      responseDraft: "",
      transcript: Object.freeze([...state.transcript, assistant]),
      usage: event.payload.usage,
      submitting: false,
    });
  }

  if (event.payload.type === "failed") {
    if (event.state !== "failed") {
      protocolError("failed payload must use failed state");
    }
    if (!["interpreting", "planning", "responding"].includes(state.sessionState)) {
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

  protocolError("speech request cannot precede completion");
}

export function reduceCompanionState(state, action) {
  switch (action.type) {
    case "session_created": {
      const session = parseSessionSnapshot(action.session);
      return Object.freeze({
        ...createCompanionState(),
        conversationId: session.conversationId,
        sessionState: session.state,
        activeTurnId: session.activeTurnId,
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
    case "harness_event":
      return reduceHarnessEvent(state, parseHarnessEvent(action.event));
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
