import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  createCompanionState,
  normalizeCompanionError,
  parseConversationBootstrap,
  parseCharacterActivation,
  parseExtractionBatchCatalog,
  parseCharacterCatalog,
  parseHarnessEvent,
  parseIntelligenceStatus,
  parseKnowledgeCatalog,
  parseModelConnectionStatus,
  parsePersonalMemoryCatalog,
  parseTurnOutcome,
  reduceCompanionState,
  parseVisualPack,
  parseVisualPackCatalog,
} from "./companionState.mjs";

const CONVERSATION_ID = "11111111-1111-4111-8111-111111111111";
const TURN_ID = "22222222-2222-4222-8222-222222222222";
const CHARACTER_ID = "33333333-3333-4333-8333-333333333333";
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

function turnEvent(turnId, sequence, state, payload) {
  return {
    conversationId: CONVERSATION_ID,
    turnId,
    sequence,
    state,
    payload,
  };
}

function bootstrap(messages = []) {
  return {
    conversation: {
      id: CONVERSATION_ID,
      characterId: CHARACTER_ID,
      createdAtUnixMs: 1,
      updatedAtUnixMs: 2,
    },
    messages,
    promptWindow: {
      conversationId: CONVERSATION_ID,
      revision: 1,
      summary: null,
      cutoffMessageSequence: 0,
      updatedAtUnixMs: 2,
    },
  };
}

function visualPack(overrides = {}) {
  return {
    schemaVersion: 2,
    packId: "fairy.atri",
    displayName: "亚托莉",
    renderer: "state_images",
    frame: { width: 128, height: 192 },
    scale: 1,
    anchor: { x: 64, y: 190 },
    states: [
      {
        id: "idle",
        description: "安静站立，适合普通等待。",
        imagePath: "/characters/atri/idle.png",
      },
      {
        id: "happy",
        description: "开心微笑，适合轻松回应。",
        imagePath: "/characters/atri/happy.png",
      },
    ],
    ...overrides,
  };
}


function assignedAppearance(pack = visualPack()) {
  return { status: "assigned", bindingRevision: 1, visual: pack };
}

function stateWithSubmission(text = "今天有点累") {
  const session = reduceCompanionState(createCompanionState(), {
    type: "session_created",
    session: bootstrap(),
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
    event: event(2, "gathering", { type: "state_changed" }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(3, "planning", { type: "state_changed" }),
  });
  return reduceCompanionState(state, {
    type: "harness_event",
    event: event(4, "responding", { type: "state_changed" }),
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
    visualState: "idle",
    chains: [
      {
        text,
        speechText: text,
        visualState: "idle",
      },
    ],
  };
}

function parsedCompiledOutcome(text = "那就先歇一会儿，我陪着你。") {
  return Object.freeze({
    conversationId: CONVERSATION_ID,
    turnId: TURN_ID,
    responseText: text,
    speechText: text,
    sources: Object.freeze([]),
    usage: Object.freeze([]),
    characterRevision: 1,
    userProfileRevision: null,
    speechRequested: true,
    visualState: "idle",
    chains: Object.freeze([
      Object.freeze({
        text,
        speechText: text,
        visualState: "idle",
      }),
    ]),
    respondMigrated: true,
  });
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
    event: event(5, "responding", {
      type: "text_delta",
      delta: "那就先歇一会儿，",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(6, "responding", {
      type: "text_delta",
      delta: "我陪着你。",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(7, "completed", completedPayload()),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(8, "completed", {
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

test("compiled reconciliation accepts parsed Wails outcome when terminal event is missing", () => {
  let state = advanceToResponding();
  assert.equal(state.activeTurnId, TURN_ID);

  state = reduceCompanionState(state, {
    type: "compiled_turn_reconciled",
    outcome: parsedCompiledOutcome("你好呀～今天也想和你高性能地在一起呢！"),
  });

  assert.equal(state.sessionState, "completed");
  assert.equal(state.activeTurnId, null);
  assert.equal(state.submitting, false);
  assert.equal(state.progressiveDraft, "");
  assert.equal(state.terminalTurn.turnId, TURN_ID);
  assert.equal(state.terminalTurn.text, "你好呀～今天也想和你高性能地在一起呢！");
  assert.equal(state.terminalTurn.speechText, "你好呀～今天也想和你高性能地在一起呢！");
  assert.equal(state.terminalTurn.characterRevision, 1);
  assert.equal(state.transcript.at(-1).role, "assistant");
  assert.equal(state.transcript.at(-1).text, "你好呀～今天也想和你高性能地在一起呢！");
  assert.equal(state.speechRequest.text, "你好呀～今天也想和你高性能地在一起呢！");
});

test("compiled reconciliation is idempotent after terminal event already completed", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "completed", completedPayload("完成文本")),
  });
  const terminal = state;

  state = reduceCompanionState(state, {
    type: "compiled_turn_reconciled",
    outcome: parsedCompiledOutcome("完成文本"),
  });

  assert.equal(state, terminal);
  assert.equal(state.activeTurnId, null);
});

test("compiled reconciliation soft-noops when submission already failed", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "invoke_failed",
    error: Object.freeze({ code: "MODEL_RESPONSE_INVALID", message: "bad", retryable: false }),
  });
  const failed = state;

  state = reduceCompanionState(state, {
    type: "compiled_turn_reconciled",
    outcome: parsedCompiledOutcome("不应覆盖失败"),
  });

  assert.equal(state, failed);
  assert.equal(state.submitting, false);
  assert.equal(state.terminalTurn, null);
});

test("RPC-first reconcile then late completed enriches usage without duplicating transcript", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "compiled_turn_reconciled",
    outcome: parsedCompiledOutcome("先应一声"),
  });
  assert.equal(state.transcript.filter((entry) => entry.role === "assistant").length, 1);
  assert.equal(state.lastSequence, 4);
  assert.deepEqual(state.usage, []);

  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "completed", {
      ...completedPayload("先应一声"),
      usage: USAGE,
    }),
  });

  assert.equal(state.sessionState, "completed");
  assert.equal(state.submitting, false);
  assert.equal(state.transcript.filter((entry) => entry.role === "assistant").length, 1);
  assert.equal(state.lastSequence, 5);
  assert.deepEqual(state.usage, USAGE);
  assert.equal(state.terminalTurn.speechText, "先应一声");
});

test("RPC-first reconcile drains leftover same-turn stream events in order", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "compiled_turn_reconciled",
    outcome: parsedCompiledOutcome("最终句"),
  });

  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "responding", {
      type: "reply_chain",
      index: 0,
      delta: "最终句",
      text: "最终句",
      speechText: "最终句",
      visualState: "idle",
    }),
  });
  assert.equal(state.lastSequence, 5);
  assert.equal(state.transcript.at(-1).text, "最终句");

  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(6, "completed", completedPayload("最终句")),
  });
  assert.equal(state.lastSequence, 6);
  assert.equal(state.transcript.filter((entry) => entry.role === "assistant").length, 1);
});

test("RPC-first reconcile accepts late speech.requested against rich terminal", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "compiled_turn_reconciled",
    outcome: {
      ...parsedCompiledOutcome("陪你一下"),
      speechRequested: false,
      speechText: "陪你一下",
    },
  });
  assert.equal(state.speechRequest, null);

  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "completed", {
      type: "speech.requested",
      text: "陪你一下",
      characterRevision: 1,
      userProfileRevision: null,
    }),
  });

  assert.equal(state.speechRequest.text, "陪你一下");
  assert.equal(state.lastSequence, 5);
});

test("RPC-first reconcile allows completed to jump a mid-stream sequence gap", () => {
  let state = advanceToResponding();
  assert.equal(state.lastSequence, 4);
  state = reduceCompanionState(state, {
    type: "compiled_turn_reconciled",
    outcome: parsedCompiledOutcome("跨序完成"),
  });
  assert.equal(state.terminalTurn.reconciled, true);

  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(7, "completed", {
      ...completedPayload("跨序完成"),
      usage: USAGE,
    }),
  });

  assert.equal(state.lastSequence, 7);
  assert.equal(state.terminalTurn.reconciled, false);
  assert.deepEqual(state.usage, USAGE);
  assert.equal(state.transcript.filter((entry) => entry.role === "assistant").length, 1);
});

test("reply chain events append draft and carry segment visual states", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "responding", {
      type: "reply_chain",
      index: 0,
      delta: "嗯，我懂。",
      text: "嗯，我懂。",
      speechText: "嗯，我懂。",
      visualState: "thinking",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(6, "responding", {
      type: "reply_chain",
      index: 1,
      delta: "\n先这样改。",
      text: "先这样改。",
      speechText: "先这样改。",
      visualState: "happy",
    }),
  });

  assert.equal(state.responseDraft, "嗯，我懂。\n先这样改。");

  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(7, "completed", {
      ...completedPayload("嗯，我懂。\n先这样改。"),
      speechText: "嗯，我懂。",
      visualState: "happy",
      chains: [
        {
          text: "嗯，我懂。",
          speechText: "嗯，我懂。",
          visualState: "thinking",
        },
        {
          text: "先这样改。",
          speechText: "先这样改。",
          visualState: "happy",
        },
      ],
    }),
  });

  assert.equal(state.responseDraft, "");
  assert.equal(state.transcript[1].text, "嗯，我懂。\n先这样改。");
  assert.equal(state.transcript[1].chains[1].visualState, "happy");
  assert.equal(state.terminalTurn.chains[0].visualState, "thinking");
});

test("out-of-order and invalid state events are dropped without crashing", () => {
  let state = stateWithSubmission();
  // Seq 2 before the turn's seq 1 is treated as a stale late event and ignored.
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(2, "interpreting", { type: "state_changed" }),
  });
  assert.equal(state.lastSequence, 0);

  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(1, "interpreting", { type: "state_changed" }),
  });
  assert.equal(state.lastSequence, 1);

  // A sequence gap must never crash the window; the bad event is dropped and the
  // last applied sequence stays put.
  const gap = reduceCompanionState(state, {
    type: "harness_event",
    event: event(3, "gathering", { type: "state_changed" }),
  });
  assert.equal(gap.lastSequence, 1);
  assert.equal(gap.sessionState, state.sessionState);

  // An invalid state transition is likewise dropped rather than thrown.
  const invalid = reduceCompanionState(state, {
    type: "harness_event",
    event: event(2, "responding", { type: "state_changed" }),
  });
  assert.equal(invalid.lastSequence, 1);
  assert.equal(invalid.sessionState, state.sessionState);
});

test("new submitted turn ignores stale late events from the previous turn", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "responding", {
      type: "reply_chain",
      index: 0,
      delta: "第一轮回复",
      text: "第一轮回复",
      speechText: "第一轮回复",
      visualState: "idle",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(6, "completed", completedPayload("第一轮回复")),
  });

  state = reduceCompanionState(state, { type: "draft_changed", value: "第二轮" });
  state = reduceCompanionState(state, { type: "submit_started", text: "第二轮" });
  // Late speech event from turn A still arrives after reset.
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(7, "completed", {
      type: "speech.synthesized",
      index: 0,
      chainIndex: 0,
      text: "第一轮回复",
      speakerId: "S_voice",
      mimeType: "audio/mpeg",
      format: "mp3",
      dataUrl: "data:audio/mpeg;base64,ZmFrZQ==",
    }),
  });
  assert.equal(state.lastSequence, 0);
  assert.equal(state.submitting, true);

  state = reduceCompanionState(state, {
    type: "harness_event",
    event: turnEvent("55555555-5555-4555-8555-555555555555", 1, "interpreting", { type: "state_changed" }),
  });
  assert.equal(state.lastSequence, 1);
  assert.equal(state.activeTurnId, "55555555-5555-4555-8555-555555555555");
});

test("duplicate harness sequence for the active turn is ignored", () => {
  let state = stateWithSubmission();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(1, "interpreting", { type: "state_changed" }),
  });
  const once = reduceCompanionState(state, {
    type: "harness_event",
    event: event(2, "gathering", { type: "state_changed" }),
  });
  const twice = reduceCompanionState(once, {
    type: "harness_event",
    event: event(2, "gathering", { type: "state_changed" }),
  });
  assert.equal(twice.lastSequence, 2);
  assert.equal(twice.sessionState, "gathering");
});

test("interrupted partial text is retained but never marked complete", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "responding", {
      type: "text_delta",
      delta: "还没有说完",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(6, "interrupted", { type: "state_changed" }),
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
    event: event(5, "responding", {
      type: "text_delta",
      delta: "部分回复",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(6, "failed", {
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

test("terminal turn drops deltas and mismatched speech; duplicate completed acks sequence", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "completed", completedPayload("完成文本")),
  });
  const terminal = state;

  const afterDelta = reduceCompanionState(terminal, {
    type: "harness_event",
    event: event(6, "responding", {
      type: "text_delta",
      delta: "越界增量",
    }),
  });
  assert.equal(afterDelta.lastSequence, terminal.lastSequence);
  assert.equal(afterDelta.responseDraft, terminal.responseDraft);

  const afterDup = reduceCompanionState(terminal, {
    type: "harness_event",
    event: event(6, "completed", completedPayload("完成文本")),
  });
  assert.equal(afterDup.lastSequence, 6);
  assert.equal(afterDup.transcript.length, terminal.transcript.length);
  assert.equal(afterDup.terminalTurn.reconciled, false);

  const afterMismatch = reduceCompanionState(terminal, {
    type: "harness_event",
    event: event(6, "completed", {
      type: "speech.requested",
      text: "不同文本",
      characterRevision: 3,
      userProfileRevision: 2,
    }),
  });
  assert.equal(afterMismatch.lastSequence, terminal.lastSequence);
  assert.equal(afterMismatch.speechRequest, terminal.speechRequest);
});

test("blank submission is rejected before a turn is created", () => {
  const session = reduceCompanionState(createCompanionState(), {
    type: "session_created",
    session: bootstrap(),
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
    visualState: "idle",
    chains: [
      {
        text: "好",
        speechText: "好。",
        visualState: "idle",
      },
    ],
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

test("completed harness usage accepts missing CacheObservation wire shape from Go", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "responding", {
      type: "reply_chain",
      index: 0,
      delta: "你好",
      text: "你好",
      speechText: "你好",
      visualState: "idle",
    }),
  });
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(6, "completed", {
      type: "completed",
      text: "你好",
      speechText: "你好",
      sources: [],
      characterRevision: 1,
      userProfileRevision: null,
      usage: [{
        lane: "respond",
        historyWindow: 1,
        usage: {
          inputTokens: 100,
          outputTokens: 50,
          cachedInputTokens: { status: "missing" },
          cacheWriteTokens: { status: "missing" },
        },
      }],
      visualState: "idle",
      chains: [{ text: "你好", speechText: "你好", visualState: "idle" }],
    }),
  });
  assert.equal(state.sessionState, "completed");
  assert.equal(state.usage[0].usage.cachedInputTokens.status, "missing");
  assert.throws(
    () => parseHarnessEvent(event(1, "completed", {
      type: "completed",
      text: "你好",
      speechText: "你好",
      sources: [],
      characterRevision: 1,
      userProfileRevision: null,
      usage: [{
        lane: "respond",
        historyWindow: 1,
        usage: {
          inputTokens: 100,
          outputTokens: 50,
          cachedInputTokens: 0,
          cacheWriteTokens: 0,
        },
      }],
      visualState: "idle",
      chains: [{ text: "你好", speechText: "你好", visualState: "idle" }],
    })),
    /cachedInputTokens must be an object/,
  );
});

test("completed reply keeps one assistant message with speech text and sources", () => {
  let state = advanceToResponding();
  state = reduceCompanionState(state, {
    type: "harness_event",
    event: event(5, "completed", {
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

test("companion app submits turns with speech enabled", () => {
  const source = readFileSync(new URL("./App.jsx", import.meta.url), "utf8");
  assert.match(source, /speechEnabled:\s*true/);
  assert.doesNotMatch(source, /speechEnabled:\s*false/);
});

test("intelligence status parser accepts public status only", () => {
  const intelligence = parseIntelligenceStatus({
    ready: true,
    schemaVersion: 1,
    summary: {
      conversations: 1,
      activeGlobalMemories: 2,
      activeCharacterMemories: 1,
      needsReviewMemories: 0,
      pendingExtractionTurns: 0,
      runningBatches: 0,
      failedBatches: 0,
      candidateKnowledge: 1,
      verifiedKnowledge: 3,
    },
    activeBackgroundJobs: 0,
    error: null,
  });

  assert.equal(intelligence.summary.verifiedKnowledge, 3);
  assert.throws(
    () => parseIntelligenceStatus({ ...intelligence, apiKey: "forbidden" }),
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
      contextWindowTokens: 128000,
      authMode: "bearer_key",
    },
    error: null,
  });

  assert.equal(status.config.model, "gpt-5.4");
  assert.equal(status.config.protocol, "responses");
  assert.equal(status.config.contextWindowTokens, 128000);
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
          contextWindowTokens: 128000,
          authMode: "bearer_key",
          promptCacheKey: true,
        },
        error: null,
      }),
    /invalid field set/,
  );
  assert.throws(
    () =>
      parseModelConnectionStatus({
        configured: true,
        ready: true,
        config: {
          protocol: "responses",
          endpoint: "https://api.openai.com/v1",
          model: "gpt-5.4",
          contextWindowTokens: 2048,
          authMode: "bearer_key",
        },
        error: null,
      }),
    /contextWindowTokens/,
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
    dialogueStyle: "短句，先接住用户当下的话。",
    textLanguage: "zh",
    speakingLanguage: "ja",
    appearance: assignedAppearance(),
  };
  const catalog = parseCharacterCatalog({
    characters: [character],
    active: character,
    diagnostics: [],
  });
  let state = stateWithSubmission();
  state = reduceCompanionState(state, { type: "session_cleared" });

  assert.deepEqual(catalog.active, character);
  assert.equal(catalog.active.dialogueStyle, "短句，先接住用户当下的话。");
  assert.equal(catalog.active.speakingLanguage, "ja");
  assert.equal(state.conversationId, null);
  assert.equal(state.transcript.length, 0);
});

test("conversation bootstrap restores ordered transcript and character activation stays atomic", () => {
  const messageId = "55555555-5555-4555-8555-555555555555";
  const assistantId = "66666666-6666-4666-8666-666666666666";
  const restored = bootstrap([
    { id: messageId, conversationId: CONVERSATION_ID, turnId: TURN_ID, sequence: 1, role: "user", content: "之前的消息", createdAtUnixMs: 1 },
    { id: assistantId, conversationId: CONVERSATION_ID, turnId: TURN_ID, sequence: 2, role: "assistant", content: "之前的回复", createdAtUnixMs: 2 },
  ]);
  const parsed = parseConversationBootstrap(restored);
  const state = reduceCompanionState(createCompanionState(), { type: "session_created", session: restored });
  assert.equal(parsed.messages.length, 2);
  assert.equal(state.characterId, CHARACTER_ID);
  assert.deepEqual(state.transcript.map(({ role, text }) => ({ role, text })), [
    { role: "user", text: "之前的消息" },
    { role: "assistant", text: "之前的回复" },
  ]);
  const activation = parseCharacterActivation({
    character: {
      characterId: CHARACTER_ID,
      revision: 1,
      name: "亚托莉",
      description: "自然回应用户。",
      dialogueStyle: null,
      textLanguage: "zh",
    speakingLanguage: "zh",
      appearance: assignedAppearance(),
    },
    session: restored,
  });
  assert.equal(activation.session.conversation.id, CONVERSATION_ID);
  assert.throws(
    () => parseConversationBootstrap({ ...restored, messages: [...restored.messages].reverse() }),
    /strictly ordered/,
  );
});

test("visual pack parser accepts exact local state image contract", () => {
  const parsed = parseVisualPack(visualPack());

  assert.equal(parsed.packId, "fairy.atri");
  assert.equal(parsed.renderer, "state_images");
  assert.equal(parsed.states[0].id, "idle");
  assert.equal(parsed.states[1].imagePath, "/characters/atri/happy.png");
  assert.equal(
    parseVisualPack({
      ...visualPack(),
      states: [
        {
          ...visualPack().states[0],
          imagePath: "fairy-character://localhost/fairy.atri/idle.png",
        },
      ],
    }).states[0].imagePath,
    "fairy-character://localhost/fairy.atri/idle.png",
  );
  assert.equal(Object.isFrozen(parsed.states), true);
  assert.deepEqual(parseVisualPackCatalog({ visualPacks: [visualPack()] }), {
    visualPacks: [parsed],
  });
});

test("visual pack parser rejects fallback-shaped and executable input", () => {
  assert.throws(
    () => parseVisualPack({ ...visualPack(), renderer: "static_png" }),
    /unsupported/,
  );
  assert.throws(
    () => parseVisualPack({
      ...visualPack(),
      states: [{ ...visualPack().states[0], imagePath: "https://example.com/idle.png" }],
    }),
    /local character PNG path/,
  );
  assert.throws(
    () => parseVisualPack({ ...visualPack(), script: "role.js" }),
    /invalid field set/,
  );
  assert.throws(
    () => parseVisualPack({
      ...visualPack(),
      states: [{ ...visualPack().states[1] }],
    }),
    /idle state/,
  );
  assert.throws(
    () => parseVisualPack({
      ...visualPack(),
      states: [visualPack().states[0], { ...visualPack().states[1], id: "idle" }],
    }),
    /unique/,
  );
  assert.throws(
    () => parseVisualPackCatalog({ visualPacks: [visualPack(), visualPack()] }),
    /duplicate/,
  );
});

test("character appearance keeps unassigned and unavailable distinct", () => {
  const base = {
    characterId: CHARACTER_ID,
    revision: 1,
    name: "旧角色",
    description: "保留原有角色身份。",
    dialogueStyle: null,
    textLanguage: "zh",
    speakingLanguage: "ja",
  };
  const catalog = parseCharacterCatalog({
    characters: [
      { ...base, appearance: { status: "unassigned" } },
      {
        ...base,
        characterId: "99999999-9999-4999-8999-999999999999",
        appearance: { status: "unavailable" },
      },
    ],
    active: null,
    diagnostics: [],
  });

  assert.equal(catalog.characters[0].appearance.status, "unassigned");
  assert.equal(catalog.characters[1].appearance.status, "unavailable");
  assert.throws(
    () => parseCharacterCatalog({
      characters: [{ ...base, appearance: { status: "assigned" } }],
      active: null,
      diagnostics: [],
    }),
    /invalid field set/,
  );
});

test("personal memory and batch catalogs reject cross-shape data", () => {
  const memoryId = "77777777-7777-4777-8777-777777777777";
  const memory = {
    id: memoryId,
    kind: "relationship",
    scope: { type: "character", characterId: CHARACTER_ID },
    reviewStatus: "ready",
    content: "用户愿意下次继续聊",
    status: "active",
    confidenceBasisPoints: 9000,
    sourceConversationId: CONVERSATION_ID,
    sourceTurnId: TURN_ID,
    supersedesId: null,
    createdAtUnixMs: 1,
    updatedAtUnixMs: 1,
  };
  const catalog = parsePersonalMemoryCatalog({ global: [], character: [memory], needsReview: [] });
  assert.equal(catalog.character[0].scope.characterId, CHARACTER_ID);
  const batches = parseExtractionBatchCatalog({ running: [], failed: [] });
  assert.equal(batches.failed.length, 0);
  assert.throws(
    () => parsePersonalMemoryCatalog({ global: [], character: [{ ...memory, apiKey: "forbidden" }], needsReview: [] }),
    /invalid field set/,
  );
});
