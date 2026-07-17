package companion

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/search"
)

func writeChatToolCall(w http.ResponseWriter, name string, argumentsJSON string) {
	payload := fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":null}]}`,
		name,
		argumentsJSON,
	)
	fmt.Fprintf(w, "data: %s\n\n", payload)
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
}

func TestParseToolQueryAndSpecs(t *testing.T) {
	query, err := parseToolQuery(`{"query":"喜欢安静"}`)
	if err != nil || query != "喜欢安静" {
		t.Fatalf("parseToolQuery = %q err=%v", query, err)
	}
	if _, err := parseToolQuery(`{"query":""}`); err == nil {
		t.Fatal("empty query should fail")
	}
	specs := RespondToolSpecs(false)
	if len(specs) != 1 || specs[0].Name != toolMemorySearch {
		t.Fatalf("memory-only specs = %#v", specs)
	}
	specs = RespondToolSpecs(true)
	if len(specs) != 2 || specs[1].Name != toolWebSearch {
		t.Fatalf("web specs = %#v", specs)
	}
	if !strings.Contains(RespondInstructionsAllowTools, toolMemorySearch) {
		t.Fatal("tool instructions missing memory_search")
	}
	if !strings.HasPrefix(RespondInstructionsAllowTools, RespondInstructions) {
		t.Fatal("tool instructions must extend RespondInstructions")
	}
}

func TestMergeRetrievalContextDedupesByID(t *testing.T) {
	merged := mergeRetrievalContext(
		memory.RetrievalContext{PersonalMemories: []memory.RetrievedPersonalMemory{{ID: "a", Content: "1"}}},
		memory.RetrievalContext{PersonalMemories: []memory.RetrievedPersonalMemory{{ID: "a", Content: "dup"}, {ID: "b", Content: "2"}}},
	)
	if len(merged.PersonalMemories) != 2 || merged.PersonalMemories[0].Content != "1" || merged.PersonalMemories[1].ID != "b" {
		t.Fatalf("merged = %#v", merged.PersonalMemories)
	}
}

func TestSubmitCompiledTurnMemoryToolThenReply(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			writeChatToolCall(w, toolMemorySearch, `{"query":"安静"}`)
			return
		}
		writeChatTextDelta(w, testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我记得你喜欢安静。"}))
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}))
	var emitted []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		emitted = append(emitted, event)
	})
	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "我今天想待着",
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d, want 2", calls.Load())
	}
	if outcome.ResponseText != "我记得你喜欢安静。" {
		t.Fatalf("outcome = %#v", outcome)
	}
	for _, event := range emitted {
		raw := fmt.Sprintf("%#v", event.Payload)
		if strings.Contains(raw, "gather") && event.State == TurnStateResponding {
			t.Fatalf("gather leaked into responding payload: %#v", event)
		}
	}
	states := make([]TurnState, 0, len(emitted))
	for _, event := range emitted {
		if payload, ok := event.Payload.(stateChangedPayload); ok && payload.Type == "state_changed" {
			states = append(states, event.State)
		} else if event.State == TurnStateCompleted {
			states = append(states, event.State)
		}
	}
	if len(states) < 4 || states[0] != TurnStateInterpreting || states[1] != TurnStateGathering || states[2] != TurnStatePlanning {
		t.Fatalf("states = %#v", states)
	}
	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	for _, message := range reloaded.Messages {
		if strings.Contains(message.Content, `"gather"`) || strings.Contains(message.Content, "memory_search") {
			t.Fatalf("transcript leaked tool/gather: %#v", message)
		}
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents() error = %v", err)
	}
	if !runtimeLedgerContainsType(ledger, runtimeLedgerEventTool) {
		t.Fatalf("ledger missing tool: %#v", ledger)
	}
}

func TestSubmitCompiledTurnRejectsGatherJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatTextDelta(w, `{"gather":{"tool":"memory.search","query":"x"}}`)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}))
	_, err = service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "你好",
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if err == nil || !strings.Contains(err.Error(), "gather JSON") {
		t.Fatalf("error = %v, want gather JSON rejection", err)
	}
}

type stubWebSearch struct {
	hits []search.Hit
}

func (s stubWebSearch) Search(ctx context.Context, query string, limit int) ([]search.Hit, error) {
	return s.hits, nil
}

func (s stubWebSearch) Close() error { return nil }

func TestSubmitCompiledTurnWebSearchToolThenReply(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			writeChatToolCall(w, toolWebSearch, `{"query":"最新动画"}`)
			return
		}
		writeChatTextDelta(w, testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "这部最近确实有更新。"}))
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	if err := config.WriteWebSearchSettings(root, config.WebSearchSettings{SchemaVersion: 1, Enabled: true}); err != nil {
		t.Fatalf("WriteWebSearchSettings() error = %v", err)
	}
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}))
	service.webSearch = stubWebSearch{hits: []search.Hit{{
		Title: "某动画更新", URL: "https://anime.example/1", Snippet: "本周新番情报",
	}}}
	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "最近那部动画怎样了",
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d, want 2", calls.Load())
	}
	if outcome.ResponseText != "这部最近确实有更新。" {
		t.Fatalf("outcome = %#v", outcome)
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeLedgerContainsType(ledger, runtimeLedgerEventTool) {
		t.Fatalf("missing tool ledger: %#v", ledger)
	}
}
