//go:build sqlite_legacy

package companion

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fairy/memory"
	"fairy/model"
)

func TestSubmitCompiledTurnEmitsHarnessLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatTextDelta(w, testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在。"}))
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4}}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	var mu sync.Mutex
	var emitted []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		mu.Lock()
		emitted = append(emitted, event)
		mu.Unlock()
	})

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "你好",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle 状态说明"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) < 5 {
		t.Fatalf("emitted = %#v", emitted)
	}
	states := make([]TurnState, 0, len(emitted))
	for _, event := range emitted {
		if event.TurnID != outcome.TurnID || event.ConversationID != outcome.ConversationID {
			t.Fatalf("event ids mismatch: %#v vs %#v", event, outcome)
		}
		states = append(states, event.State)
	}
	if states[0] != TurnStateInterpreting || states[1] != TurnStateGathering || states[2] != TurnStatePlanning || states[3] != TurnStateResponding {
		t.Fatalf("prefix states = %#v", states[:4])
	}
	if states[len(states)-1] != TurnStateCompleted {
		t.Fatalf("terminal state = %#v", states)
	}
	foundReply := false
	for _, event := range emitted {
		if payload, ok := event.Payload.(replyChainPayload); ok && payload.Type == "reply_chain" {
			foundReply = true
			if payload.Text != "我在。" {
				t.Fatalf("reply payload = %#v", payload)
			}
		}
	}
	if !foundReply {
		t.Fatal("missing reply_chain event")
	}
	wire, err := json.Marshal(struct {
		Events  []HarnessEvent `json:"events"`
		Outcome TurnOutcome    `json:"outcome"`
	}{Events: emitted, Outcome: outcome})
	if err != nil {
		t.Fatalf("json.Marshal(wire) error = %v", err)
	}
	for _, forbidden := range []string{`"decision"`, `"stance"`, `"replyIntent"`, `"tone"`, `"relationshipSignal"`, `"replyMode"`} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("wire payload leaked %s: %s", forbidden, wire)
		}
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents() error = %v", err)
	}
	assertRuntimeLedgerNoForbidden(t, ledger)
	for _, eventType := range []string{
		runtimeLedgerEventTransition,
		runtimeLedgerEventGather,
		runtimeLedgerEventPrompt,
		runtimeLedgerEventContinuation,
		runtimeLedgerEventModel,
		runtimeLedgerEventCompile,
		runtimeLedgerEventTerminal,
	} {
		if !runtimeLedgerContainsType(ledger, eventType) {
			t.Fatalf("ledger missing %s: %#v", eventType, ledger)
		}
	}
	if !runtimeLedgerMetadataContains(ledger, runtimeLedgerEventPrompt, "contextSlots") || !runtimeLedgerMetadataContains(ledger, runtimeLedgerEventPrompt, "promptInputHash") {
		t.Fatalf("prompt ledger missing slot/hash metadata: %#v", ledger)
	}
	if !runtimeLedgerMetadataContains(ledger, runtimeLedgerEventContinuation, "incremental") || !runtimeLedgerMetadataContains(ledger, runtimeLedgerEventModel, "providerCacheObservation") {
		t.Fatalf("continuation/model ledger incomplete: %#v", ledger)
	}
	if !runtimeLedgerTerminalStatus(ledger, "completed") {
		t.Fatalf("terminal ledger missing completed status: %#v", ledger)
	}
}

func TestSubmitCompiledTurnMapsObservedCacheUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatTextDelta(w, testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在。"}))
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":6},"cache_write_input_tokens":2}}`+"\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	var emitted []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		emitted = append(emitted, event)
	})

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "你好",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle 状态说明"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
	var completed *completedPayload
	for _, event := range emitted {
		if payload, ok := event.Payload.(completedPayload); ok {
			completed = &payload
		}
	}
	if completed == nil || len(completed.Usage) != 1 {
		t.Fatalf("completed usage missing: emitted=%#v", emitted)
	}
	usage := completed.Usage[0].Usage
	if usage.CachedInputTokens.Status != "observed" || usage.CachedInputTokens.Tokens == nil || *usage.CachedInputTokens.Tokens != 6 {
		t.Fatalf("cached input observation = %#v", usage.CachedInputTokens)
	}
	if usage.CacheWriteTokens.Status != "observed" || usage.CacheWriteTokens.Tokens == nil || *usage.CacheWriteTokens.Tokens != 2 {
		t.Fatalf("cache write observation = %#v", usage.CacheWriteTokens)
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents() error = %v", err)
	}
	if !runtimeLedgerMetadataContains(ledger, runtimeLedgerEventModel, `"cachedInputTokens":{"status":"observed","tokens":6}`) ||
		!runtimeLedgerMetadataContains(ledger, runtimeLedgerEventModel, `"cacheWriteTokens":{"status":"observed","tokens":2}`) {
		t.Fatalf("model ledger missing observed cache usage: %#v", ledger)
	}
	window, ok, err := memoryStore.LoadContextWindow(outcome.ConversationID, string(model.PromptLaneRespond))
	if err != nil {
		t.Fatalf("LoadContextWindow() error = %v", err)
	}
	if !ok || window.ObservedPrefillTokens == nil || *window.ObservedPrefillTokens != 11 || window.LastTrigger != contextWindowTriggerCompletedUsage || window.PromptWindowRevision != bootstrap.PromptWindow.Revision {
		t.Fatalf("context window = %#v ok=%v", window, ok)
	}
	if !runtimeLedgerMetadataContains(ledger, runtimeLedgerEventContextWindow, `"observedPrefillTokens":11`) ||
		!runtimeLedgerMetadataContains(ledger, runtimeLedgerEventContextWindow, `"lastTrigger":"completed_usage"`) {
		t.Fatalf("context window ledger missing observed prefill: %#v", ledger)
	}
}

func TestSubmitCompiledTurnInvalidReplyFailsFromPlanningWithoutAssistant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatTextDelta(w, `{"chains":[{"visualState":"idle","text":"不该成功。"}],"unexpected":true}`)
		writeChatStop(w)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	var mu sync.Mutex
	var emitted []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		mu.Lock()
		emitted = append(emitted, event)
		mu.Unlock()
	})

	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "你好",
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if submitErr == nil {
		t.Fatal("SubmitCompiledTurn() error = nil, want invalid reply error")
	}

	mu.Lock()
	states := make([]TurnState, 0, len(emitted))
	for _, event := range emitted {
		states = append(states, event.State)
		if _, ok := event.Payload.(replyChainPayload); ok {
			t.Fatalf("invalid reply emitted reply chain: %#v", event)
		}
	}
	mu.Unlock()
	if len(states) != 4 || states[0] != TurnStateInterpreting || states[1] != TurnStateGathering || states[2] != TurnStatePlanning || states[3] != TurnStateFailed {
		t.Fatalf("states = %#v, want interpreting/gathering/planning/failed", states)
	}
	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" {
		t.Fatalf("messages = %#v, want only persisted user", reloaded.Messages)
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(bootstrap.Conversation.ID, reloaded.Messages[0].TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents() error = %v", err)
	}
	assertRuntimeLedgerNoForbidden(t, ledger)
	if !runtimeLedgerTerminalCode(ledger, "MODEL_RESPONSE_INVALID") {
		t.Fatalf("terminal ledger missing MODEL_RESPONSE_INVALID: %#v", ledger)
	}
	if service.ActiveBackgroundJobs() != 0 {
		t.Fatalf("background jobs = %d, want 0", service.ActiveBackgroundJobs())
	}
	if record, ok, err := memoryStore.LoadLaneContinuation(bootstrap.Conversation.ID, string(model.PromptLaneRespond)); err != nil {
		t.Fatalf("LoadLaneContinuation() error = %v", err)
	} else if ok {
		t.Fatalf("continuation record = %#v, want nil", record)
	}
	if !strings.Contains(submitErr.Error(), "strict reply chains") {
		t.Fatalf("error = %v, want explicit strict reply chains failure", submitErr)
	}
	if errors.Is(submitErr, ErrTurnInterrupted) {
		t.Fatalf("error = %v, must not be interruption", submitErr)
	}
}

func assertRuntimeLedgerNoForbidden(t *testing.T, events []memory.TurnRuntimeEventRecord) {
	t.Helper()
	wire, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("json.Marshal(ledger) error = %v", err)
	}
	for _, forbidden := range []string{`"decision"`, `"stance"`, `"replyIntent"`, `"tone"`, `"relationshipSignal"`, `"replyMode"`, `"reasoning"`, `"analysis"`, `"rationale"`, "Authorization", "Bearer "} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("runtime ledger leaked %s: %s", forbidden, wire)
		}
	}
}

func runtimeLedgerContainsType(events []memory.TurnRuntimeEventRecord, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}

func runtimeLedgerMetadataContains(events []memory.TurnRuntimeEventRecord, eventType string, fragment string) bool {
	for _, event := range events {
		if event.EventType == eventType && strings.Contains(event.MetadataJSON, fragment) {
			return true
		}
	}
	return false
}

func runtimeLedgerTerminalStatus(events []memory.TurnRuntimeEventRecord, status string) bool {
	for _, event := range events {
		if event.EventType == runtimeLedgerEventTerminal && strings.Contains(event.MetadataJSON, `"status":"`+status+`"`) {
			return true
		}
	}
	return false
}

func runtimeLedgerTerminalCode(events []memory.TurnRuntimeEventRecord, code string) bool {
	for _, event := range events {
		if event.EventType == runtimeLedgerEventTerminal && event.Code != nil && *event.Code == code {
			return true
		}
	}
	return false
}
