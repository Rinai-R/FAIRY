//go:build sqlite_legacy

package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/config"
	"fairy/memory"
	"fairy/memory/semantic"
	"fairy/model"
	"fairy/search"
)

func waitForBackgroundJobs(t *testing.T, service *CompanionService) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if service.ActiveBackgroundJobs() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background jobs still active: %d", service.ActiveBackgroundJobs())
}

type companionSemanticFakeEmbedder struct {
	ready bool
	dims  int
}

func (f companionSemanticFakeEmbedder) Ready() bool { return f.ready }

func (f companionSemanticFakeEmbedder) Status() semantic.Status {
	if f.ready {
		return semantic.StatusReady
	}
	return semantic.StatusUnavailable
}

func (f companionSemanticFakeEmbedder) Dims() int {
	if f.dims != 0 {
		return f.dims
	}
	return memory.SemanticEmbeddingDimensions
}

func (f companionSemanticFakeEmbedder) Embed(texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vector := make([]float32, f.Dims())
		if len(vector) > 0 {
			vector[0] = 1
		}
		vectors[index] = vector
	}
	return vectors, nil
}

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
	for _, want := range []string{"profile", "preferences", "experiences", "current-character relationship", "confirmed local knowledge", "semanticStatus"} {
		if !strings.Contains(specs[0].Description, want) {
			t.Fatalf("memory tool description missing %q: %q", want, specs[0].Description)
		}
	}
	if !strings.Contains(specs[0].Description, "FTS-only") {
		t.Fatalf("memory tool description missing layered metadata: %q", specs[0].Description)
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
		memory.RetrievalContext{PersonalMemories: []memory.RetrievedPersonalMemory{{ID: "a", Content: "1"}}, SemanticStatus: "unavailable"},
		memory.RetrievalContext{PersonalMemories: []memory.RetrievedPersonalMemory{{ID: "a", Content: "dup"}, {ID: "b", Content: "2"}}, SemanticStatus: "ready"},
	)
	if len(merged.PersonalMemories) != 2 || merged.PersonalMemories[0].Content != "1" || merged.PersonalMemories[1].ID != "b" {
		t.Fatalf("merged = %#v", merged.PersonalMemories)
	}
	if merged.SemanticStatus != "ready" {
		t.Fatalf("semantic status = %q", merged.SemanticStatus)
	}
}

func TestRetrievalFromWebHitsUsesKnowledgeLayer(t *testing.T) {
	ctx := retrievalFromWebHits([]search.Hit{{
		Title: "某动画更新",
		URL:   "https://anime.example/1",
	}})
	if len(ctx.Knowledge) != 1 {
		t.Fatalf("knowledge = %#v", ctx.Knowledge)
	}
	if ctx.Knowledge[0].Layer != "knowledge" || ctx.SemanticStatus != "unavailable" {
		t.Fatalf("metadata = layer %q semantic %q", ctx.Knowledge[0].Layer, ctx.SemanticStatus)
	}
}

func TestRetrieveMemoryForToolUsesSemanticWhenReady(t *testing.T) {
	root := t.TempDir()
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation(seed) error = %v", err)
	}
	seedTurn, err := memoryStore.BeginTurn(bootstrap.Conversation.ID, "我喜欢咖啡")
	if err != nil {
		t.Fatalf("BeginTurn(seed) error = %v", err)
	}
	if _, err := memoryStore.CompleteTurn(bootstrap.Conversation.ID, seedTurn.ID, "我记住了。"); err != nil {
		t.Fatalf("CompleteTurn(seed) error = %v", err)
	}
	if _, err := memoryStore.CreatePersonalMemory("preference", memory.MemoryScope{Type: "global"}, "用户喜欢咖啡", 9000); err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	embedder := companionSemanticFakeEmbedder{ready: true}
	if result, err := memoryStore.ProcessEmbeddingJobs(embedder, 10); err != nil || result.Succeeded != 1 {
		t.Fatalf("ProcessEmbeddingJobs() = %#v, %v", result, err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{}, nil), nil)
	AttachSemanticEmbedder(service, embedder)
	ctx, err := service.retrieveMemoryForTool(characterID, "咖啡")
	if err != nil {
		t.Fatalf("retrieveMemoryForTool() error = %v", err)
	}
	if ctx.SemanticStatus != string(semantic.StatusUsed) {
		t.Fatalf("semantic status = %q, want used", ctx.SemanticStatus)
	}
	if len(ctx.PersonalMemories) != 1 || ctx.PersonalMemories[0].Layer != "preference" {
		t.Fatalf("semantic memories = %#v", ctx.PersonalMemories)
	}
}

func TestRetrieveMemoryForToolUsesSemanticAPIEmbedder(t *testing.T) {
	var embeddingCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("embedding path = %q, want /v1/embeddings", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode embedding request: %v", err)
		}
		if body["model"] != "text-embedding-3-small" || int(body["dimensions"].(float64)) != memory.SemanticEmbeddingDimensions || body["encoding_format"] != "float" {
			t.Fatalf("embedding request = %#v", body)
		}
		input, ok := body["input"].([]any)
		if !ok || len(input) != 1 || strings.TrimSpace(input[0].(string)) == "" {
			t.Fatalf("embedding input = %#v", body["input"])
		}
		embeddingCalls.Add(1)
		vector := make([]float32, memory.SemanticEmbeddingDimensions)
		vector[0] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"index":     0,
				"embedding": vector,
			}},
		})
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL+"/v1", "no_auth")
	if err := config.WriteSemanticEmbeddingSettings(root, config.SemanticEmbeddingSettings{
		Enabled:    true,
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
	}); err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v", err)
	}
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation(seed) error = %v", err)
	}
	seedTurn, err := memoryStore.BeginTurn(bootstrap.Conversation.ID, "我喜欢咖啡")
	if err != nil {
		t.Fatalf("BeginTurn(seed) error = %v", err)
	}
	if _, err := memoryStore.CompleteTurn(bootstrap.Conversation.ID, seedTurn.ID, "我记住了。"); err != nil {
		t.Fatalf("CompleteTurn(seed) error = %v", err)
	}
	if _, err := memoryStore.CreatePersonalMemory("preference", memory.MemoryScope{Type: "global"}, "用户喜欢咖啡", 9000); err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	settings, err := config.ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	modelService := model.NewModelService(root, nil)
	embedder, err := modelService.SemanticAPIEmbedder(settings)
	if err != nil {
		t.Fatalf("SemanticAPIEmbedder() error = %v", err)
	}
	if result, err := memoryStore.ProcessEmbeddingJobs(embedder, 10); err != nil || result.Succeeded != 1 {
		t.Fatalf("ProcessEmbeddingJobs() = %#v, %v", result, err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, modelService, nil)
	AttachSemanticEmbedder(service, embedder)
	ctx, err := service.retrieveMemoryForTool(characterID, "咖啡")
	if err != nil {
		t.Fatalf("retrieveMemoryForTool() error = %v", err)
	}
	if ctx.SemanticStatus != string(semantic.StatusUsed) {
		t.Fatalf("semantic status = %q, want used", ctx.SemanticStatus)
	}
	if len(ctx.PersonalMemories) != 1 || ctx.PersonalMemories[0].Content != "用户喜欢咖啡" {
		t.Fatalf("semantic memories = %#v", ctx.PersonalMemories)
	}
	if embeddingCalls.Load() < 2 {
		t.Fatalf("embedding calls = %d, want job + query", embeddingCalls.Load())
	}
}

func TestRetrieveMemoryForToolFallsBackWhenSemanticUnavailable(t *testing.T) {
	root := t.TempDir()
	memoryStore, characterID := seedCompanionRuntime(t, root)
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{}, nil), nil)
	AttachSemanticEmbedder(service, companionSemanticFakeEmbedder{})
	ctx, err := service.retrieveMemoryForTool(characterID, "咖啡")
	if err != nil {
		t.Fatalf("retrieveMemoryForTool() error = %v", err)
	}
	if ctx.SemanticStatus != string(semantic.StatusUnavailable) {
		t.Fatalf("semantic status = %q, want unavailable", ctx.SemanticStatus)
	}
	if len(ctx.PersonalMemories) != 0 || len(ctx.Knowledge) != 0 {
		t.Fatalf("fallback should not fabricate short-query hits: %#v", ctx)
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
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
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
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
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
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
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
	waitForBackgroundJobs(t, service)
}
