import test from "node:test";
import assert from "node:assert/strict";

import {
  createCompanionState,
  normalizeCompanionError,
  parseCharacterCatalog,
  parseHarnessEvent,
  parseModelConnectionStatus,
  parseTurnOutcome,
  reduceCompanionState,
} from "./companionState.mjs";

const CONVERSATION_ID = "11111111-1111-4111-8111-111111111111";
const TURN_ID = "22222222-2222-4222-8222-222222222222";

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
    characterRevision: 3,
    userProfileRevision: 2,
    usage: USAGE,
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

test("model status parser accepts public fields only and errors stay structured", () => {
  const status = parseModelConnectionStatus({
    configured: true,
    ready: true,
    config: {
      endpoint: "https://api.openai.com/v1",
      model: "gpt-5.4",
      authMode: "bearer_key",
      promptCacheKey: true,
      cachedTokensUsage: true,
    },
    error: null,
  });

  assert.equal(status.config.model, "gpt-5.4");
  assert.equal("apiKey" in status.config, false);
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
