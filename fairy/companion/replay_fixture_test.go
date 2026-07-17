package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"fairy/model"
)

type replayFixtureTransport struct {
	mu             sync.Mutex
	respondBatches [][]model.StreamEvent
	compactBatches [][]model.StreamEvent
	requests       []model.RequestDraft
}

func (t *replayFixtureTransport) Execute(ctx context.Context, draft model.RequestDraft, bearerKey string, onEvent func(model.StreamEvent)) error {
	t.mu.Lock()
	t.requests = append(t.requests, draft)
	var batch []model.StreamEvent
	switch {
	case strings.Contains(draft.BodyJSON, ExtractInstructions):
		batch = replayTextEvents("", "{\"mutations\":[]}", nil, nil, nil)
	case strings.Contains(draft.BodyJSON, CompactInstructions):
		if len(t.compactBatches) == 0 {
			t.mu.Unlock()
			return errors.New("replay fixture missing compact batch")
		}
		batch = append([]model.StreamEvent(nil), t.compactBatches[0]...)
		t.compactBatches = t.compactBatches[1:]
	default:
		if len(t.respondBatches) == 0 {
			t.mu.Unlock()
			return errors.New("replay fixture missing respond batch")
		}
		batch = append([]model.StreamEvent(nil), t.respondBatches[0]...)
		t.respondBatches = t.respondBatches[1:]
	}
	t.mu.Unlock()

	for _, event := range batch {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			onEvent(event)
		}
	}
	return nil
}

func (t *replayFixtureTransport) respondRequestBodies() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	bodies := make([]string, 0, len(t.requests))
	for _, request := range t.requests {
		if !strings.Contains(request.BodyJSON, ExtractInstructions) && !strings.Contains(request.BodyJSON, CompactInstructions) {
			bodies = append(bodies, request.BodyJSON)
		}
	}
	return bodies
}

func replayTextEvents(responseID string, text string, promptTokens *int, cachedInputTokens *uint64, cacheWriteTokens *uint64) []model.StreamEvent {
	events := []model.StreamEvent{{Type: "text_delta", Data: text}}
	if promptTokens != nil || cachedInputTokens != nil || cacheWriteTokens != nil {
		usage := &model.Usage{CachedInputTokens: cachedInputTokens, CacheWriteTokens: cacheWriteTokens}
		if promptTokens != nil {
			usage.PromptTokens = *promptTokens
			usage.CompletionTokens = 4
		}
		events = append(events, model.StreamEvent{Type: "usage", Usage: usage})
	}
	events = append(events, model.StreamEvent{Type: "completed", Data: responseID})
	return events
}

func intPtr(value int) *int {
	return &value
}

func uint64Ptr(value uint64) *uint64 {
	return &value
}

func assertNoForbiddenRuntimeText(t *testing.T, label string, value string) {
	t.Helper()
	for _, forbidden := range []string{"\\\"decision\\\"", "\\\"stance\\\"", "\\\"replyIntent\\\"", "\\\"tone\\\"", "\\\"relationshipSignal\\\"", "\\\"replyMode\\\"", "\\\"reasoning\\\"", "\\\"analysis\\\"", "\\\"rationale\\\"", "Authorization", "Bearer "} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("%s leaked %s: %s", label, forbidden, value)
		}
	}
}

func TestReplayFixtureHarnessCoversSuccessContinuationCacheCompactionAndNoLeak(t *testing.T) {
	root := t.TempDir()
	writeModelConnectionCapabilities(t, root, "responses", "https://fixture.invalid", true)
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	transport := &replayFixtureTransport{
		respondBatches: [][]model.StreamEvent{
			replayTextEvents("resp_replay_1", testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "第一轮回复。"}), intPtr(11), uint64Ptr(0), uint64Ptr(5)),
			replayTextEvents("resp_replay_2", testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "第二轮回复。"}), intPtr(13), uint64Ptr(7), uint64Ptr(3)),
		},
		compactBatches: [][]model.StreamEvent{
			replayTextEvents("resp_compact_1", "用户连续问候，角色持续回应在场。", nil, nil, nil),
		},
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, transport))

	var emittedMu sync.Mutex
	var emitted []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		emittedMu.Lock()
		emitted = append(emitted, event)
		emittedMu.Unlock()
	})

	first, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "第一轮",
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if err != nil {
		t.Fatalf("first SubmitCompiledTurn() error = %v", err)
	}
	second, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "第二轮",
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if err != nil {
		t.Fatalf("second SubmitCompiledTurn() error = %v", err)
	}
	if first.ResponseText != "第一轮回复。" || second.ResponseText != "第二轮回复。" {
		t.Fatalf("outcomes = %#v / %#v", first, second)
	}

	respondBodies := transport.respondRequestBodies()
	if len(respondBodies) != 2 {
		t.Fatalf("respond request count = %d bodies=%#v", len(respondBodies), respondBodies)
	}
	if strings.Contains(respondBodies[0], "previous_response_id") {
		t.Fatalf("first request unexpectedly continued: %s", respondBodies[0])
	}
	if !strings.Contains(respondBodies[1], "\"previous_response_id\":\"resp_replay_1\"") || !strings.Contains(respondBodies[1], "第二轮") || strings.Contains(respondBodies[1], "第一轮回复") {
		t.Fatalf("second request is not deterministic suffix continuation: %s", respondBodies[1])
	}

	secondLedger, err := memoryStore.ListTurnRuntimeEvents(second.ConversationID, second.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents(second) error = %v", err)
	}
	assertRuntimeLedgerNoForbidden(t, secondLedger)
	if !runtimeLedgerMetadataContains(secondLedger, runtimeLedgerEventContinuation, "\"incremental\":true") ||
		!runtimeLedgerMetadataContains(secondLedger, runtimeLedgerEventModel, "\"cachedInputTokens\":{\"status\":\"observed\",\"tokens\":7}") ||
		!runtimeLedgerMetadataContains(secondLedger, runtimeLedgerEventModel, "\"cacheWriteTokens\":{\"status\":\"observed\",\"tokens\":3}") {
		t.Fatalf("second replay ledger incomplete: %#v", secondLedger)
	}
	emittedMu.Lock()
	for _, event := range emitted {
		wire, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("json.Marshal(harness event) error = %v", err)
		}
		assertNoForbiddenRuntimeText(t, "harness event", string(wire))
	}
	emittedMu.Unlock()
	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	for _, message := range reloaded.Messages {
		assertNoForbiddenRuntimeText(t, "persisted message", message.Content)
	}

	if _, ok, err := memoryStore.LoadLaneContinuation(bootstrap.Conversation.ID, string(model.PromptLaneRespond)); err != nil {
		t.Fatalf("LoadLaneContinuation(before compact) error = %v", err)
	} else if !ok {
		t.Fatal("continuation missing before compaction boundary")
	}
	result, err := service.CompactConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("CompactConversation() error = %v", err)
	}
	if result.WindowRevision != 2 || result.RetainedDialogueItems != 4 {
		t.Fatalf("compaction result = %#v", result)
	}
	if record, ok, err := memoryStore.LoadLaneContinuation(bootstrap.Conversation.ID, string(model.PromptLaneRespond)); err != nil {
		t.Fatalf("LoadLaneContinuation(after compact) error = %v", err)
	} else if ok {
		t.Fatalf("continuation not cleared at compaction boundary: %#v", record)
	}
	window, ok, err := memoryStore.LoadContextWindow(bootstrap.Conversation.ID, string(model.PromptLaneRespond))
	if err != nil {
		t.Fatalf("LoadContextWindow(after compact) error = %v", err)
	}
	if !ok || window.PromptWindowRevision != result.WindowRevision || window.LastTrigger != contextWindowTriggerCompactionCommit || window.PreviousWindowID == nil {
		t.Fatalf("context window after replay compaction = %#v ok=%v", window, ok)
	}
}

func TestReplayFixtureHarnessRejectsDecisionLeak(t *testing.T) {
	root := t.TempDir()
	writeModelConnectionCapabilities(t, root, "responses", "https://fixture.invalid", true)
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	transport := &replayFixtureTransport{
		respondBatches: [][]model.StreamEvent{
			replayTextEvents("resp_leak", "{\"decision\":{\"stance\":\"bad\"},\"chains\":[{\"visualState\":\"idle\",\"text\":\"不该成功。\"}]}", intPtr(9), nil, nil),
		},
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, transport))
	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "触发泄漏",
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if submitErr == nil || !strings.Contains(submitErr.Error(), "strict reply chains") {
		t.Fatalf("SubmitCompiledTurn() error = %v, want strict leak rejection", submitErr)
	}
	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" {
		t.Fatalf("messages after rejected leak = %#v", reloaded.Messages)
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(bootstrap.Conversation.ID, reloaded.Messages[0].TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents() error = %v", err)
	}
	assertRuntimeLedgerNoForbidden(t, ledger)
	if !runtimeLedgerTerminalCode(ledger, "MODEL_RESPONSE_INVALID") {
		t.Fatalf("terminal ledger missing invalid model response: %#v", ledger)
	}
}
