package model

import (
	"encoding/json"
	"strings"
	"testing"

	"fairy/config"
)

func connection(protocol Protocol) Connection {
	capabilities := config.GatewayCapabilities{
		CachedTokensUsage: true,
	}
	if protocol == ProtocolResponses {
		capabilities.PromptCacheKey = true
	}
	return Connection{
		Protocol:            protocol,
		Endpoint:            "https://api.deepseek.com/v1",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "bearer_key",
		Capabilities:        capabilities,
	}
}

func request() CompiledPromptRequest {
	return CompiledPromptRequest{
		Shape: ModelRequestShape{
			Lane:            PromptLaneRespond,
			Model:           "deepseek-v4-flash",
			Instructions:    "stable instructions",
			MaxOutputTokens: 160,
			PromptCacheKey:  "fairy:conversation:respond",
		},
		Input: []PromptItem{
			{Type: PromptItemUserMessage, Content: "你好"},
			{Type: PromptItemAssistantMessage, Content: "我在"},
		},
	}
}

func bodyMap(t *testing.T, draft RequestDraft) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(draft.BodyJSON), &body); err != nil {
		t.Fatalf("BodyJSON is not JSON: %v", err)
	}
	return body
}

func TestConnectionFromStatusRequiresConfiguredModel(t *testing.T) {
	_, err := ConnectionFromStatus(config.ModelConnectionStatus{Configured: false})
	if err == nil {
		t.Fatal("ConnectionFromStatus() error = nil, want explicit unconfigured error")
	}

	conn, err := ConnectionFromStatus(config.ModelConnectionStatus{
		Configured:          true,
		Protocol:            "chat_completions",
		Endpoint:            "https://api.deepseek.com",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "bearer_key",
		Capabilities: config.GatewayCapabilities{
			CachedTokensUsage: true,
		},
	})
	if err != nil {
		t.Fatalf("ConnectionFromStatus() error = %v", err)
	}
	if conn.Protocol != ProtocolChatCompletions {
		t.Fatalf("Protocol = %q", conn.Protocol)
	}
}

func TestConnectionFromConfigRequiresInternalConnectionID(t *testing.T) {
	_, err := ConnectionFromConfig(config.ModelConnection{
		Protocol:            "chat_completions",
		Endpoint:            "https://api.deepseek.com",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "bearer_key",
	})
	if err == nil {
		t.Fatal("ConnectionFromConfig() error = nil, want missing connection_id error")
	}

	conn, err := ConnectionFromConfig(config.ModelConnection{
		ConnectionID:        "connection-1",
		Protocol:            "chat_completions",
		Endpoint:            "https://api.deepseek.com",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "bearer_key",
		Capabilities: config.GatewayCapabilities{
			CachedTokensUsage: true,
		},
	})
	if err != nil {
		t.Fatalf("ConnectionFromConfig() error = %v", err)
	}
	if conn.Protocol != ProtocolChatCompletions || conn.Model != "deepseek-v4-flash" {
		t.Fatalf("connection = %#v", conn)
	}
}

func TestBuildResponsesRequestDraftMatchesOpenAICompatibleShape(t *testing.T) {
	draft, err := BuildRequestDraft(connection(ProtocolResponses), request())
	if err != nil {
		t.Fatalf("BuildRequestDraft() error = %v", err)
	}
	body := bodyMap(t, draft)

	if draft.Method != "POST" {
		t.Fatalf("Method = %q", draft.Method)
	}
	if draft.URL != "https://api.deepseek.com/v1/responses" {
		t.Fatalf("URL = %q", draft.URL)
	}
	if draft.AuthRequirement != AuthRequirementBearerKey {
		t.Fatalf("AuthRequirement = %q", draft.AuthRequirement)
	}
	if body["model"] != "deepseek-v4-flash" {
		t.Fatalf("model = %v", body["model"])
	}
	if body["instructions"] != "stable instructions" {
		t.Fatalf("instructions = %v", body["instructions"])
	}
	if body["stream"] != true || body["store"] != false {
		t.Fatalf("stream/store = %v/%v", body["stream"], body["store"])
	}
	if body["prompt_cache_key"] != "fairy:conversation:respond" {
		t.Fatalf("prompt_cache_key = %v", body["prompt_cache_key"])
	}
	if _, ok := body["tools"]; ok {
		t.Fatal("Responses body must not include tools when none requested")
	}
	req := request()
	req.Tools = []ToolSpec{{
		Name:        "memory_search",
		Description: "Search personal memories",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	}}
	draftWithTools, err := BuildRequestDraft(connection(ProtocolResponses), req)
	if err != nil {
		t.Fatalf("BuildRequestDraft(tools) error = %v", err)
	}
	bodyWithTools := bodyMap(t, draftWithTools)
	tools, ok := bodyWithTools["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", bodyWithTools["tools"])
	}
	if strings.Contains(draft.BodyJSON, "sk-") || strings.Contains(strings.ToLower(draft.BodyJSON), "authorization") {
		t.Fatalf("BodyJSON leaked secret-shaped data: %s", draft.BodyJSON)
	}
	input := body["input"].([]any)
	if input[0].(map[string]any)["role"] != "user" || input[0].(map[string]any)["content"] != "你好" {
		t.Fatalf("unexpected first input item: %#v", input[0])
	}
	if input[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("unexpected assistant role: %#v", input[1])
	}
	if !strings.Contains(input[1].(map[string]any)["content"].(string), "\"visualState\":\"idle\"") {
		t.Fatalf("assistant history was not reply-chain encoded: %#v", input[1])
	}
}

func TestParticipationLaneUsesJSONResponseFormatForChatCompletions(t *testing.T) {
	req := request()
	req.Shape.Lane = PromptLaneParticipate
	req.Shape.PromptCacheKey = ""
	draft, err := BuildRequestDraft(connection(ProtocolChatCompletions), req)
	if err != nil {
		t.Fatal(err)
	}
	body := bodyMap(t, draft)
	format, ok := body["response_format"].(map[string]any)
	if !ok || format["type"] != "json_object" {
		t.Fatalf("response_format = %#v", body["response_format"])
	}
}

func TestBuildResponsesRequestDraftSupportsContinuationSuffix(t *testing.T) {
	req := request()
	req.Input = []PromptItem{{Type: PromptItemUserMessage, Content: "新增问题"}}
	req.PreviousResponseID = "resp_previous"
	draft, err := BuildRequestDraft(connection(ProtocolResponses), req)
	if err != nil {
		t.Fatalf("BuildRequestDraft() error = %v", err)
	}
	body := bodyMap(t, draft)
	if body["previous_response_id"] != "resp_previous" {
		t.Fatalf("previous_response_id = %v", body["previous_response_id"])
	}
	if len(body["input"].([]any)) != 1 {
		t.Fatalf("input length = %d", len(body["input"].([]any)))
	}
}

func TestBuildChatCompletionsRequestDraftMatchesDeepSeekJSONShape(t *testing.T) {
	draft, err := BuildRequestDraft(connection(ProtocolChatCompletions), request())
	if err != nil {
		t.Fatalf("BuildRequestDraft() error = %v", err)
	}
	body := bodyMap(t, draft)
	if draft.URL != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("URL = %q", draft.URL)
	}
	if body["stream"] != true {
		t.Fatalf("stream = %v", body["stream"])
	}
	if body["prompt_cache_key"] != nil {
		t.Fatalf("Chat body unexpectedly included prompt_cache_key")
	}
	if body["previous_response_id"] != nil {
		t.Fatalf("Chat body unexpectedly included previous_response_id")
	}
	if body["thinking"] != nil {
		t.Fatalf("chat body must not vendor-probe thinking: %#v", body["thinking"])
	}
	if body["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("response_format = %#v", body["response_format"])
	}
	if body["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("stream_options = %#v", body["stream_options"])
	}
	messages := body["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1].(map[string]any)["content"] != "你好" {
		t.Fatalf("second message = %#v", messages[1])
	}
	if !strings.Contains(messages[2].(map[string]any)["content"].(string), "\"chains\"") {
		t.Fatalf("assistant message was not reply-chain JSON: %#v", messages[2])
	}
}

func TestBuildChatCompletionsTranslateKeepsProviderNeutralBody(t *testing.T) {
	req := CompiledPromptRequest{
		Shape: ModelRequestShape{
			Lane:            PromptLaneTranslate,
			Model:           "deepseek-v4-flash",
			Instructions:    "translate",
			MaxOutputTokens: 1024,
			PromptCacheKey:  "fairy:conversation:translate",
		},
		Input: []PromptItem{
			{Type: PromptItemContextData, Content: `{"contextType":"character","name":"亚托莉"}`},
			{Type: PromptItemUserMessage, Content: "Source language: Chinese (zh)\nTarget speaking language: Japanese (ja)\n\n嗯。"},
		},
	}
	draft, err := BuildRequestDraft(connection(ProtocolChatCompletions), req)
	if err != nil {
		t.Fatalf("BuildRequestDraft() error = %v", err)
	}
	body := bodyMap(t, draft)
	if body["thinking"] != nil {
		t.Fatalf("translate body must not inject vendor thinking controls: %#v", body["thinking"])
	}
	if body["response_format"] != nil {
		t.Fatalf("translate should not force json_object: %#v", body["response_format"])
	}
	if body["max_tokens"] != float64(1024) {
		t.Fatalf("max_tokens = %#v", body["max_tokens"])
	}
}

func TestBuildRequestDraftRejectsUnsafeOrMismatchedInput(t *testing.T) {
	tests := []struct {
		name string
		conn Connection
		req  CompiledPromptRequest
	}{
		{
			name: "model mismatch",
			conn: connection(ProtocolResponses),
			req: func() CompiledPromptRequest {
				req := request()
				req.Shape.Model = "other-model"
				return req
			}(),
		},
		{
			name: "missing responses cache key",
			conn: connection(ProtocolResponses),
			req: func() CompiledPromptRequest {
				req := request()
				req.Shape.PromptCacheKey = ""
				return req
			}(),
		},
		{
			name: "chat continuation",
			conn: connection(ProtocolChatCompletions),
			req: func() CompiledPromptRequest {
				req := request()
				req.PreviousResponseID = "resp_previous"
				return req
			}(),
		},
		{
			name: "resource endpoint",
			conn: func() Connection {
				conn := connection(ProtocolResponses)
				conn.Endpoint = "https://api.deepseek.com/v1/responses"
				return conn
			}(),
			req: request(),
		},
		{
			name: "unknown prompt item",
			conn: connection(ProtocolResponses),
			req: func() CompiledPromptRequest {
				req := request()
				req.Input = []PromptItem{{Type: "tool_call", Content: "{}"}}
				return req
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BuildRequestDraft(tt.conn, tt.req); err == nil {
				t.Fatal("BuildRequestDraft() error = nil, want error")
			}
		})
	}
}

func TestNonRespondChatRequestStaysPlainText(t *testing.T) {
	req := request()
	req.Shape.Lane = PromptLaneCompact
	draft, err := BuildRequestDraft(connection(ProtocolChatCompletions), req)
	if err != nil {
		t.Fatalf("BuildRequestDraft() error = %v", err)
	}
	body := bodyMap(t, draft)
	if _, ok := body["response_format"]; ok {
		t.Fatal("non-respond Chat request must not force JSON response_format")
	}
	messages := body["messages"].([]any)
	if messages[2].(map[string]any)["content"] != "我在" {
		t.Fatalf("non-respond assistant history should remain plain text: %#v", messages[2])
	}
}
