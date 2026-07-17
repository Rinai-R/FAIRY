package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func transportDraft(url string, protocol Protocol, auth AuthRequirement) RequestDraft {
	body := `{"model":"deepseek-v4-flash","stream":true,"instructions":"hi","input":[{"role":"user","content":"ping"}],"max_output_tokens":16,"store":false,"text":{"format":{"type":"text"}}}`
	if protocol == ProtocolChatCompletions {
		body = `{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"ping"}],"max_tokens":16,"stream_options":{"include_usage":true}}`
	}
	return RequestDraft{
		Protocol:        protocol,
		Method:          "POST",
		URL:             url,
		ContentType:     "application/json",
		AuthRequirement: auth,
		BodyJSON:        body,
	}
}

func protocolRequestURL(serverURL string, protocol Protocol) string {
	if protocol == ProtocolChatCompletions {
		return strings.TrimRight(serverURL, "/") + "/chat/completions"
	}
	return strings.TrimRight(serverURL, "/") + "/responses"
}

func executeWithServer(t *testing.T, handler http.HandlerFunc, protocol Protocol, auth AuthRequirement) ([]StreamEvent, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	var events []StreamEvent
	err := SDKTransport{HTTPClient: server.Client()}.Execute(
		t.Context(),
		transportDraft(protocolRequestURL(server.URL, protocol), protocol, auth),
		"sk-test-secret",
		func(event StreamEvent) {
			events = append(events, event)
		},
	)
	return events, err
}

const responsesCompletedSSE = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"

func TestNewModelServiceDefaultsToSDKTransport(t *testing.T) {
	service := NewModelService(t.TempDir(), nil)
	if _, ok := service.transport.(SDKTransport); !ok {
		t.Fatalf("default transport = %T, want SDKTransport", service.transport)
	}
}

func TestExecuteRequestPostsToCorrectURL(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responsesCompletedSSE)
	}, ProtocolResponses, AuthRequirementBearerKey)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestExecuteRequestIncludesBearerAuth(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test-secret" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responsesCompletedSSE)
	}, ProtocolResponses, AuthRequirementBearerKey)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestExecuteRequestOmitsAuthForNone(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responsesCompletedSSE)
	}, ProtocolResponses, AuthRequirementNone)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestExecuteRequestParsesResponsesSSE(t *testing.T) {
	events, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"你\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"好\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_abc\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"你好\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
	}, ProtocolResponses, AuthRequirementNone)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != "text_delta" || events[0].Data != "你" || events[1].Data != "好" {
		t.Fatalf("text events = %#v", events[:2])
	}
	if events[2].Type != "usage" || events[2].Usage.PromptTokens != 3 || events[2].Usage.CompletionTokens != 2 {
		t.Fatalf("usage event = %#v", events[2])
	}
	if events[3].Type != "completed" || events[3].Data != "resp_abc" {
		t.Fatalf("completed event = %#v", events[3])
	}
}

func TestExecuteRequestParsesResponsesCacheUsage(t *testing.T) {
	events, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","delta":"好"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_cache","output":[{"type":"message","content":[{"text":"好"}]}],"usage":{"input_tokens":9,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":3},"output_tokens":2}}}`+"\n\n")
	}, ProtocolResponses, AuthRequirementNone)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(events) != 3 || events[1].Type != "usage" || events[1].Usage == nil {
		t.Fatalf("events = %#v", events)
	}
	usage := events[1].Usage
	if usage.PromptTokens != 9 || usage.CompletionTokens != 2 {
		t.Fatalf("usage token counts = %#v", usage)
	}
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 4 {
		t.Fatalf("cached input tokens = %#v", usage.CachedInputTokens)
	}
	if usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 3 {
		t.Fatalf("cache write tokens = %#v", usage.CacheWriteTokens)
	}
}

func TestExecuteRequestParsesChatCompletionsSSE(t *testing.T) {
	events, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
	}, ProtocolChatCompletions, AuthRequirementNone)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Data != "你" || events[1].Data != "好" {
		t.Fatalf("text events = %#v", events[:2])
	}
	if events[2].Type != "usage" || events[2].Usage.PromptTokens != 3 || events[2].Usage.CompletionTokens != 2 {
		t.Fatalf("usage event = %#v", events[2])
	}
	if events[3].Type != "completed" {
		t.Fatalf("completed event = %#v", events[3])
	}
}

func TestExecuteRequestParsesChatCompletionsCacheUsage(t *testing.T) {
	events, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"好"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":7},"cache_write_input_tokens":2}}`+"\n\n")
	}, ProtocolChatCompletions, AuthRequirementNone)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(events) != 3 || events[1].Type != "usage" || events[1].Usage == nil {
		t.Fatalf("events = %#v", events)
	}
	usage := events[1].Usage
	if usage.PromptTokens != 11 || usage.CompletionTokens != 3 {
		t.Fatalf("usage token counts = %#v", usage)
	}
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 7 {
		t.Fatalf("cached input tokens = %#v", usage.CachedInputTokens)
	}
	if usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 2 {
		t.Fatalf("cache write tokens = %#v", usage.CacheWriteTokens)
	}
}

func TestExecuteRequestHandlesStreamDone(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}, ProtocolResponses, AuthRequirementNone)
	if err == nil || !strings.Contains(err.Error(), "IncompleteStream") {
		t.Fatalf("Responses stream ending before completed must fail, got %v", err)
	}
}

func TestExecuteRequestChatCompletionsDoneCompletes(t *testing.T) {
	events, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	}, ProtocolChatCompletions, AuthRequirementNone)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(events) < 2 || events[len(events)-1].Type != "completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestExecuteRequestReturnsErrorOnNon2xx(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}, ProtocolResponses, AuthRequirementBearerKey)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestExecuteRequestReturnsErrorOnMalformedSSE(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "not-sse\n")
	}, ProtocolResponses, AuthRequirementNone)
	if err == nil {
		t.Fatal("Execute() error = nil, want malformed SSE error")
	}
}

func TestExecuteRequestRejectsEmptyURL(t *testing.T) {
	err := SDKTransport{}.Execute(t.Context(), transportDraft("", ProtocolResponses, AuthRequirementNone), "", nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
}

func TestExecuteRequestDoesNotLeakKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key sk-test-secret", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	err := SDKTransport{HTTPClient: server.Client()}.Execute(
		context.Background(),
		transportDraft(protocolRequestURL(server.URL, ProtocolResponses), ProtocolResponses, AuthRequirementBearerKey),
		"sk-test-secret",
		nil,
	)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if strings.Contains(err.Error(), "sk-test-secret") {
		t.Fatalf("error leaked key: %q", err.Error())
	}
}

func TestExecuteRequestRequiresBearerKeyWhenNeeded(t *testing.T) {
	err := SDKTransport{}.Execute(t.Context(), transportDraft("https://example.test/responses", ProtocolResponses, AuthRequirementBearerKey), "", nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want missing bearer error")
	}
}
