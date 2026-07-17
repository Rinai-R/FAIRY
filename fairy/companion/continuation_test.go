package companion

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fairy/memory"
	"fairy/model"
)

func isRespondLaneInstructions(instructions string) bool {
	return instructions == RespondInstructions ||
		instructions == RespondInstructionsAllowTools ||
		strings.HasPrefix(instructions, RespondInstructions)
}

func writeModelConnectionCapabilities(t *testing.T, root string, protocol string, endpoint string, cacheRetention bool) {
	t.Helper()
	dir := filepath.Join(root, "model")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	document := fmt.Sprintf(
		`{"schema_version":1,"data":{"schema_version":3,"connection_id":"6a129284-6358-47b0-ad64-2a5907d36c91","protocol":%q,"endpoint":%q,"model":"deepseek-v4-flash","context_window_tokens":1048576,"auth_mode":"no_auth","capabilities":{"prompt_cache_key":true,"cached_tokens_usage":true,"explicit_breakpoints":false,"cache_retention":%t,"websocket_continuation":false}}}`,
		protocol,
		endpoint,
		cacheRetention,
	)
	if err := os.WriteFile(filepath.Join(dir, "connection.json"), []byte(document), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func TestSubmitCompiledTurnUsesSuffixContinuationWhenCacheRetentionSupported(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		turn := len(bodies)
		mu.Unlock()
		if instructions, _ := body["instructions"].(string); instructions == ExtractInstructions {
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"mutations\\\":[]}\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_extract\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"{\\\"mutations\\\":[]}\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			return
		}
		text := fmt.Sprintf("第%d轮回复", turn)
		payload := testRespondEnvelope(testReplyChain{VisualState: "idle", Text: text})
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", payload)
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":%q}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n", turn, payload)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionCapabilities(t, root, "responses", server.URL, true)
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	states := []VisualState{{ID: "idle", Description: "idle 状态说明"}}

	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "第一轮",
		MaxOutputTokens:       160,
		AvailableVisualStates: states,
	}); err != nil {
		t.Fatalf("first turn error = %v", err)
	}
	if record, ok, err := memoryStore.LoadLaneContinuation(bootstrap.Conversation.ID, string(model.PromptLaneRespond)); err != nil {
		t.Fatalf("LoadLaneContinuation() after first turn error = %v", err)
	} else if !ok || record.PreviousResponseID != "resp_1" || record.RequestShapeHash == "" || record.InputPrefixHash == "" || record.ResponseItemHash == "" {
		t.Fatalf("persisted continuation record = %#v ok=%t", record, ok)
	}
	service = NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	secondOutcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "第二轮",
		MaxOutputTokens:       160,
		AvailableVisualStates: states,
	})
	if err != nil {
		t.Fatalf("second turn error = %v", err)
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(secondOutcome.ConversationID, secondOutcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents(second) error = %v", err)
	}
	metadata := runtimeLedgerMetadataForType(t, ledger, runtimeLedgerEventContinuation)
	if metadata["incremental"] != true || metadata["previousStateSource"] != "sqlite_lane_continuations" {
		t.Fatalf("second continuation metadata = %#v", metadata)
	}

	mu.Lock()
	defer mu.Unlock()
	respondBodies := make([]map[string]any, 0, 2)
	for _, body := range bodies {
		if instructions, _ := body["instructions"].(string); isRespondLaneInstructions(instructions) {
			respondBodies = append(respondBodies, body)
		}
	}
	if len(respondBodies) != 2 {
		t.Fatalf("respond bodies = %d, want 2; all=%d", len(respondBodies), len(bodies))
	}
	if _, ok := respondBodies[0]["previous_response_id"]; ok {
		t.Fatalf("first turn unexpectedly continued: %#v", respondBodies[0])
	}
	if respondBodies[1]["previous_response_id"] != "resp_1" {
		t.Fatalf("second turn previous_response_id = %#v", respondBodies[1]["previous_response_id"])
	}
	input, ok := respondBodies[1]["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("second turn input = %#v", respondBodies[1]["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "第二轮" {
		t.Fatalf("second turn suffix = %#v", first)
	}
	if respondBodies[0]["prompt_cache_key"] != "fairy:"+bootstrap.Conversation.ID+":respond" {
		t.Fatalf("prompt_cache_key = %#v", respondBodies[0]["prompt_cache_key"])
	}
}

func TestDecideContinuationFromPersistentHashesUsesDistinctReasons(t *testing.T) {
	root := t.TempDir()
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, nil, nil)
	shape := model.ModelRequestShape{
		Lane:            model.PromptLaneRespond,
		Model:           "deepseek-v4-flash",
		Instructions:    RespondInstructions,
		MaxOutputTokens: 160,
		PromptCacheKey:  model.LaneCacheKey(bootstrap.Conversation.ID, model.PromptLaneRespond),
	}
	previousInput := []model.PromptItem{{Type: model.PromptItemUserMessage, Content: "第一轮"}}
	assistantItem := []model.PromptItem{{Type: model.PromptItemAssistantMessage, Content: "第一轮回复"}}
	if _, err := memoryStore.SaveLaneContinuation(memory.LaneContinuationRecord{
		ConversationID:     bootstrap.Conversation.ID,
		Lane:               string(model.PromptLaneRespond),
		PreviousResponseID: "resp_1",
		RequestShapeHash:   runtimeHash(shape),
		InputPrefixHash:    runtimeHash(previousInput),
		ResponseItemHash:   runtimeHash(assistantItem),
		WindowRevision:     bootstrap.PromptWindow.Revision,
	}); err != nil {
		t.Fatalf("SaveLaneContinuation() error = %v", err)
	}

	current := model.CompiledPromptRequest{
		Shape: shape,
		Input: []model.PromptItem{
			previousInput[0],
			assistantItem[0],
			{Type: model.PromptItemUserMessage, Content: "第二轮"},
		},
	}
	decision, previous, err := service.decideContinuation(bootstrap.Conversation.ID, true, bootstrap.PromptWindow.Revision, current)
	if err != nil {
		t.Fatalf("decideContinuation(incremental) error = %v", err)
	}
	if previous == nil || !decision.Incremental || decision.PreviousResponseID != "resp_1" || len(decision.NewItems) != 1 || decision.NewItems[0].Content != "第二轮" {
		t.Fatalf("incremental decision = %#v previous=%#v", decision, previous)
	}
	if decision, _, err := service.decideContinuation(bootstrap.Conversation.ID, false, bootstrap.PromptWindow.Revision, current); err != nil || decision.FullReason != model.ContinuationCapabilityUnsupported {
		t.Fatalf("capability unsupported = %#v err=%v", decision, err)
	}
	changedShape := current
	changedShape.Shape.MaxOutputTokens = 200
	if decision, _, err := service.decideContinuation(bootstrap.Conversation.ID, true, bootstrap.PromptWindow.Revision, changedShape); err != nil || decision.FullReason != model.ContinuationRequestShapeChanged {
		t.Fatalf("shape changed = %#v err=%v", decision, err)
	}
	mismatch := current
	mismatch.Input = []model.PromptItem{{Type: model.PromptItemUserMessage, Content: "重写第一轮"}, assistantItem[0], {Type: model.PromptItemUserMessage, Content: "第二轮"}}
	if decision, _, err := service.decideContinuation(bootstrap.Conversation.ID, true, bootstrap.PromptWindow.Revision, mismatch); err != nil || decision.FullReason != model.ContinuationPrefixMismatch {
		t.Fatalf("prefix mismatch = %#v err=%v", decision, err)
	}
	notExtended := current
	notExtended.Input = []model.PromptItem{previousInput[0], assistantItem[0]}
	if decision, _, err := service.decideContinuation(bootstrap.Conversation.ID, true, bootstrap.PromptWindow.Revision, notExtended); err != nil || decision.FullReason != model.ContinuationInputNotExtended {
		t.Fatalf("not extended = %#v err=%v", decision, err)
	}
	if err := memoryStore.ClearLaneContinuation(bootstrap.Conversation.ID, string(model.PromptLaneRespond)); err != nil {
		t.Fatalf("ClearLaneContinuation() error = %v", err)
	}
	if decision, previous, err := service.decideContinuation(bootstrap.Conversation.ID, true, bootstrap.PromptWindow.Revision, current); err != nil || previous != nil || decision.FullReason != model.ContinuationNoPreviousState {
		t.Fatalf("no previous state = %#v previous=%#v err=%v", decision, previous, err)
	}
}

func runtimeLedgerMetadataForType(t *testing.T, events []memory.TurnRuntimeEventRecord, eventType string) map[string]any {
	t.Helper()
	for _, event := range events {
		if event.EventType != eventType {
			continue
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
			t.Fatalf("json.Unmarshal(%s metadata) error = %v", eventType, err)
		}
		return metadata
	}
	t.Fatalf("missing runtime ledger event %s: %#v", eventType, events)
	return nil
}
