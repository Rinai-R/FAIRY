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

func TestParseCognitionOutputGatherAndReply(t *testing.T) {
	kind, req, err := ParseCognitionOutput(`{"gather":{"tool":"memory.search","query":"喜欢安静"}}`)
	if err != nil {
		t.Fatalf("ParseCognitionOutput(gather) error = %v", err)
	}
	if kind != CognitionGather || req.Tool != gatherToolMemorySearch || req.Query != "喜欢安静" {
		t.Fatalf("gather = %v %#v", kind, req)
	}
	kind, _, err = ParseCognitionOutput(testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在。"}))
	if err != nil || kind != CognitionReply {
		t.Fatalf("reply kind = %v err=%v", kind, err)
	}
	if _, _, err := ParseCognitionOutput(`{"gather":{"tool":"vector.knn","query":"x"}}`); err == nil || !strings.Contains(err.Error(), "whitelisted") {
		t.Fatalf("non-whitelist error = %v", err)
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

func TestSubmitCompiledTurnOptionalMemoryGatherThenReply(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			writeChatTextDelta(w, `{"gather":{"tool":"memory.search","query":"安静"}}`)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
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
		if strings.Contains(message.Content, `"gather"`) || strings.Contains(message.Content, "memory.search") {
			t.Fatalf("transcript leaked gather: %#v", message)
		}
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents() error = %v", err)
	}
	if !runtimeLedgerContainsType(ledger, runtimeLedgerEventGather) {
		t.Fatalf("ledger missing gather: %#v", ledger)
	}
}

func TestSubmitCompiledTurnRejectsNonWhitelistGather(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatTextDelta(w, `{"gather":{"tool":"shell.exec","query":"ls"}}`)
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
	if err == nil || !strings.Contains(err.Error(), "whitelisted") {
		t.Fatalf("error = %v, want whitelist rejection", err)
	}
}

func TestRespondInstructionsAllowGatherMentionsMemorySearchOnly(t *testing.T) {
	if !strings.Contains(RespondInstructionsAllowGather, gatherToolMemorySearch) {
		t.Fatal("AllowGather missing memory.search")
	}
	if strings.Contains(RespondInstructionsAllowGather, gatherToolWebSearch) {
		t.Fatal("memory-only AllowGather must not advertise web.search")
	}
	if !strings.Contains(RespondGatherInstructions(true), gatherToolWebSearch) {
		t.Fatal("web-enabled gather instructions missing web.search")
	}
	if !strings.HasPrefix(RespondInstructionsAllowGather, RespondInstructions) {
		t.Fatal("AllowGather must extend RespondInstructions")
	}
}

type stubWebSearch struct {
	hits []search.Hit
}

func (s stubWebSearch) Search(ctx context.Context, query string, limit int) ([]search.Hit, error) {
	return s.hits, nil
}

func (s stubWebSearch) Close() error { return nil }

func TestSubmitCompiledTurnWebSearchGatherThenReply(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			writeChatTextDelta(w, `{"gather":{"tool":"web.search","query":"最新动画"}}`)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
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
	if !runtimeLedgerContainsType(ledger, runtimeLedgerEventGather) {
		t.Fatalf("missing gather ledger: %#v", ledger)
	}
}
