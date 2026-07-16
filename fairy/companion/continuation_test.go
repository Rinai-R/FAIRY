package companion

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"fairy/model"
)

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
		payload := fmt.Sprintf(`{"chains":[{"visualState":"idle","text":%q}]}`, text)
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
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.HTTPTransport{Client: server.Client()}))
	states := []VisualState{{ID: "idle", Description: "idle 状态说明"}}

	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "第一轮",
		MaxOutputTokens:       160,
		AvailableVisualStates: states,
	}); err != nil {
		t.Fatalf("first turn error = %v", err)
	}
	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "第二轮",
		MaxOutputTokens:       160,
		AvailableVisualStates: states,
	}); err != nil {
		t.Fatalf("second turn error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	respondBodies := make([]map[string]any, 0, 2)
	for _, body := range bodies {
		if instructions, _ := body["instructions"].(string); instructions == RespondInstructions {
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