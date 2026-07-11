import test from "node:test";
import assert from "node:assert/strict";

import {
  createCompanionState,
  normalizeCompanionError,
  parseCharacterCatalog,
  parseHarnessEvent,
  parseIntelligenceStatus,
  parseKnowledgeCatalog,
  parseModelConnectionStatus,
  parseSearchConnectionStatus,
  parseTurnOutcome,
  reduceCompanionState,
} from "./companionState.mjs";

const CONVERSATION_ID = "11111111-1111-4111-8111-111111111111";
const TURN_ID = "22222222-2222-4222-8222-222222222222";
const KNOWLEDGE_ID = "44444444-4444-4444-8444-444444444444";

const USAGE = Object.freeze([
  {
    lane: "respond",
    historyWindow: 1,
    usage: {
      inputTokens: 120,
      outputTokens: 12,
      cachedInputTokens: { status: "observed", tokens: 80 },
      cacheWriteTokens: { status: "unsupported" },
    },
  },
]);

function event(sequence, state, payload) {
  return {
    conversationId: CONVERSATION_ID,
    turnId: TURN_ID,
    sequence,
    state,
    payload,
  };
}

function stateWithSubmission(text = "今天有点累") {
  const session = reduceCompanionState(createCompanionState(), {
    type: "session_created",
    session: {
      conversationId: CONVERSATION_ID,
      state: "idle",
      activeTurnId: null,
    },
  });
  return reduceCompanionState(session, {
    type: "submit_started",
    text,
  });
}

function advanceToResponding(initial = stateWithSubmission()) {
  let state = initial;
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(1, "interpreting", { type: "state_changed" }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(2, "planning", { type: "state_changed" }),
  });
  return reduceCompanionState(state, {
    type: "harness_event",
    event: event(3, "responding", { type: "state_changed" }),
  });
}

function completedPayload(text = "那就先歇一会儿，我陪着你。") {
  return {
    type: "completed",
    text,
    speechText: text,
    sources: [],
    characterRevision: 3,
    userProfileRevision: 2,
    usage: USAGE,
  };
}

function knowledgeRecord(overrides = {}) {
  return {
    id: KNOWLEDGE_ID,
    topic: "项目版本",
    statement: "FAIRY 的知识库使用 SQLite schema v2。",
    status: "candidate",
    verificationBasis: "unverified",
    confidenceBasisPoints: 8500,
    sourceConversationId: CONVERSATION_ID,
    sourceTurnId: TURN_ID,
    supersedesId: null,
    sources: [],
    createdAtUnixMs: 1_700_000_000_000,
    updatedAtUnixMs: 1_700_000_000_001,
    ...overrides,
  };
}

test("normal stream produces one completed transcript and matching speech request", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(4, "responding", {
      type: "text_delta",
      delta: "那就先歇一会儿，",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "responding", {
      type: "text_delta",
      delta: "我陪着你。",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(6, "completed", completedPayload()),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(7, "completed", {
      type: "speech.requested",
      text: "那就先歇一会儿，我陪着你。",
      characterRevision: 3,
      userProfileRevision: 2,
    }),
  });

  assert.equal(state.sessionState, "completed");
  assert.equal(state.activeTurnId, null);
  assert.equal(state.responseDraft, "");
  assert.deepEqual(
    state.transcript.map(({ role, text, status }) => ({ role, text, status })),
    [
      { role: "user", text: "今天有点累", status: "completed" },
      {
        role: "assistant",
        text: "那就先歇一会儿，我陪着你。",
        status: "completed",
      },
    ],
  );
  assert.equal(state.speechRequest.text, state.terminalTurn.text);
  assert.deepEqual(state.usage, USAGE);
});

test("out-of-order and invalid state events are rejected", () => {
  const state = stateWithSubmission();

  assert.throws(
    () =>
      reduceCompanionState(state, {
        type: "harness_event",
        event: event(2, "interpreting", { type: "state_changed" }),
      }),
    /duplicated or out of order/,
  );
  assert.throws(
    () =>
      reduceCompanionState(state, {
        type: "harness_event",
        event: event(1, "responding", { type: "state_changed" }),
      }),
    /state transition is invalid/,
  );
});

test("interrupted partial text is retained but never marked complete", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(4, "responding", {
      type: "text_delta",
      delta: "还没有说完",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "interrupted", { type: "state_changed" }),
  });

  assert.equal(state.sessionState, "interrupted");
  assert.equal(state.responseDraft, "还没有说完");
  assert.equal(state.transcript.length, 1);
  assert.equal(state.speechRequest, null);
});

test("failed stream preserves diagnostics without promoting partial text", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(4, "responding", {
      type: "text_delta",
      delta: "部分回复",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "failed", {
      type: "failed",
      error: {
        code: "MODEL_STREAM_FAILED",
        message: "模型流中断",
        retryable: true,
      },
    }),
  });

  assert.equal(state.responseDraft, "部分回复");
  assert.equal(state.transcript.length, 1);
  assert.deepEqual(state.error, {
    code: "MODEL_STREAM_FAILED",
    message: "模型流中断",
    retryable: true,
  });
});

test("terminal turn rejects deltas, duplicate completion, and mismatched speech", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(4, "completed", completedPayload("完成文本")),
  });

  assert.throws(
    () =>
      reduceCompanionState(state, {
        type: "harness_event",
        event: event(5, "responding", {
          type: "text_delta",
          delta: "越界增量",
        }),
      }),
    /terminal turn cannot accept/,
  );
  assert.throws(
    () =>
      reduceCompanionState(state, {
        type: "harness_event",
        event: event(5, "completed", completedPayload("完成文本")),
      }),
    /terminal turn cannot accept/,
  );
  assert.throws(
    () =>
      reduceCompanionState(state, {
        type: "harness_event",
        event: event(5, "completed", {
          type: "speech.requested",
          text: "不同文本",
          characterRevision: 3,
          userProfileRevision: 2,
        }),
      }),
    /does not match/,
  );
});

test("blank submission is rejected before a turn is created", () => {
  const session = reduceCompanionState(createCompanionState(), {
    type: "session_created",
    session: {
      conversationId: CONVERSATION_ID,
      state: "idle",
      activeTurnId: null,
    },
  });

  assert.throws(
    () =>
      reduceCompanionState(session, {
        type: "submit_started",
        text: "   ",
      }),
    /不能为空/,
  );
});

test("event and outcome parsers reject missing fields and preserve zero cache hits", () => {
  const parsed = parseHarnessEvent(
    event(1, "completed", {
      ...completedPayload("好"),
      usage: [
        {
          ...USAGE[0],
          usage: {
            ...USAGE[0].usage,
            cachedInputTokens: { status: "observed", tokens: 0 },
          },
        },
      ],
    }),
  );
  const outcome = parseTurnOutcome({
    conversationId: CONVERSATION_ID,
    turnId: TURN_ID,
    responseText: "好",
    speechText: "好。",
    sources: [],
    characterRevision: 1,
    userProfileRevision: null,
    usage: parsed.payload.usage,
    speechRequested: false,
  });

  assert.equal(
    outcome.usage[0].usage.cachedInputTokens.tokens,
    0,
  );
  assert.throws(
    () => parseHarnessEvent({ ...event(1, "interpreting", {}), extra: true }),
    /invalid field set/,
  );
});

test("completed reply keeps one assistant message with speech text and sources", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(4, "completed", {
      ...completedPayload("今天的更新已经发布，晚点我再陪你细看。"),
      speechText: "今天的更新已经发布。",
      sources: [
        {
          title: "发布说明",
          url: "https://example.com/release",
          snippet: "官方发布说明",
          rank: 1,
          fetchedAtUnixMs: 1_700_000_000_000,
        },
      ],
    }),
  });

  assert.equal(state.transcript.length, 2);
  assert.equal(state.transcript[1].speechText, "今天的更新已经发布。");
  assert.equal(state.transcript[1].sources[0].title, "发布说明");
  assert.throws(
    () => parseHarnessEvent(event(1, "completed", {
      ...completedPayload("好。"),
      sources: [{
        title: "坏来源",
        url: "javascript:alert(1)",
        snippet: "不应接受",
        rank: 1,
        fetchedAtUnixMs: 1,
      }],
    })),
    /HTTP or HTTPS/,
  );
});

test("search and intelligence status parsers accept public status only", () => {
  const search = parseSearchConnectionStatus({
    configured: true,
    ready: true,
    config: {
      provider: "brave",
      endpoint: "https://api.search.brave.com/res/v1/web/search",
    },
    error: null,
  });
  const intelligence = parseIntelligenceStatus({
    ready: true,
    schemaVersion: 1,
    summary: {
      activePersonalMemories: 2,
      candidateKnowledge: 1,
      verifiedKnowledge: 3,
      pendingJobs: 0,
      runningJobs: 0,
      failedJobs: 0,
    },
    activeBackgroundJobs: 0,
    error: null,
  });

  assert.equal(search.config.provider, "brave");
  assert.equal(intelligence.summary.verifiedKnowledge, 3);
  assert.throws(
    () => parseSearchConnectionStatus({ ...search, apiKey: "forbidden" }),
    /invalid field set/,
  );
});

test("knowledge catalog accepts only coherent candidate and verified records", () => {
  const source = {
    title: "FAIRY 发布说明",
    url: "https://example.com/fairy-v2",
    snippet: "本地知识库升级到 schema v2。",
    rank: 1,
    fetchedAtUnixMs: 1_700_000_000_000,
  };
  const catalog = parseKnowledgeCatalog({
    candidates: [knowledgeRecord()],
    verified: [knowledgeRecord({
      id: "55555555-5555-4555-8555-555555555555",
      status: "verified",
      verificationBasis: "web_source",
      sources: [source],
    })],
  });

  assert.equal(catalog.candidates[0].verificationBasis, "unverified");
  assert.equal(catalog.verified[0].sources[0].url, source.url);
  assert.equal(Object.isFrozen(catalog.verified), true);
  assert.throws(
    () => parseKnowledgeCatalog({
      candidates: [knowledgeRecord({ sources: [source] })],
      verified: [],
    }),
    /source-free/,
  );
  assert.throws(
    () => parseKnowledgeCatalog({
      candidates: [],
      verified: [knowledgeRecord({
        status: "verified",
        verificationBasis: "web_source",
      })],
    }),
    /must include sources/,
  );
  assert.throws(
    () => parseKnowledgeCatalog({
      candidates: [],
      verified: [knowledgeRecord({
        status: "verified",
        verificationBasis: "confidence_threshold",
      })],
    }),
    /unsupported/,
  );
  assert.throws(
    () => parseKnowledgeCatalog({
      candidates: [knowledgeRecord({ apiKey: "forbidden" })],
      verified: [],
    }),
    /invalid field set/,
  );
});

test("knowledge catalog refuses oversized result sets", () => {
  const candidates = Array.from({ length: 21 }, (_, index) => knowledgeRecord({
    id: `44444444-4444-4444-8444-${String(index).padStart(12, "0")}`,
  }));
  assert.throws(
    () => parseKnowledgeCatalog({ candidates, verified: [] }),
    /at most 20/,
  );
});

test("model status parser accepts public fields only and errors stay structured", () => {
  const status = parseModelConnectionStatus({
    configured: true,
    ready: true,
    config: {
      protocol: "responses",
      endpoint: "https://api.openai.com/v1",
      model: "gpt-5.4",
      authMode: "bearer_key",
    },
    error: null,
  });

  assert.equal(status.config.model, "gpt-5.4");
  assert.equal(status.config.protocol, "responses");
  assert.equal("apiKey" in status.config, false);
  assert.equal("promptCacheKey" in status.config, false);
  assert.throws(
    () =>
      parseModelConnectionStatus({
        configured: true,
        ready: true,
        config: {
          protocol: "responses",
          endpoint: "https://api.openai.com/v1",
          model: "gpt-5.4",
          authMode: "bearer_key",
          promptCacheKey: true,
        },
        error: null,
      }),
    /invalid field set/,
  );
  assert.deepEqual(
    normalizeCompanionError({
      code: "MODEL_AUTH_FAILED",
      message: "模型认证失败",
      retryable: false,
    }),
    {
      code: "MODEL_AUTH_FAILED",
      message: "模型认证失败",
      retryable: false,
    },
  );
  assert.equal(
    normalizeCompanionError("Bearer secret-leak").message.includes("secret-leak"),
    false,
  );
});

test("character catalog exposes the active revision and session can be cleared", () => {
  const character = {
    characterId: "33333333-3333-4333-8333-333333333333",
    revision: 2,
    name: "亚托莉",
    description: "来自海边的仿生少女。",
  };
  const catalog = parseCharacterCatalog({
    characters: [character],
    active: character,
    diagnostics: [],
  });
  let state = stateWithSubmission();
  state = reduceCompanionState(state, { type: "session_cleared" });

  assert.deepEqual(catalog.active, character);
  assert.equal(state.conversationId, null);
  assert.equal(state.transcript.length, 0);
});
