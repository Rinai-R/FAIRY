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
	return RequestDraft{
		Protocol:        protocol,
		Method:          "POST",
		URL:             url,
		ContentType:     "application/json",
		AuthRequirement: auth,
		BodyJSON:        `{"model":"deepseek-v4-flash","stream":true}`,
	}
}

func executeWithServer(t *testing.T, handler http.HandlerFunc, protocol Protocol, auth AuthRequirement) ([]StreamEvent, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	var events []StreamEvent
	err := HTTPTransport{Client: server.Client()}.Execute(t.Context(), transportDraft(server.URL, protocol, auth), "sk-test-secret", func(event StreamEvent) {
		events = append(events, event)
	})
	return events, err
}

const responsesCompletedSSE = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"

func TestExecuteRequestPostsToCorrectURL(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept = %q", r.Header.Get("Accept"))
		}
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

func TestExecuteRequestParsesChatCompletionsSSE(t *testing.T) {
	events, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
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

func TestExecuteRequestHandlesStreamDone(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: [DONE]\n\n")
	}, ProtocolResponses, AuthRequirementNone)
	if err == nil || !strings.Contains(err.Error(), "IncompleteStream") {
		t.Fatalf("Responses stream ending before completed must fail, got %v", err)
	}
}

func TestExecuteRequestChatCompletionsDoneCompletes(t *testing.T) {
	events, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
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
	if !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestExecuteRequestReturnsErrorOnMalformedSSE(t *testing.T) {
	_, err := executeWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not-sse\n")
	}, ProtocolResponses, AuthRequirementNone)
	if err == nil {
		t.Fatal("Execute() error = nil, want malformed SSE error")
	}
}

func TestExecuteRequestRejectsEmptyURL(t *testing.T) {
	err := HTTPTransport{}.Execute(t.Context(), transportDraft("", ProtocolResponses, AuthRequirementNone), "", nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
}

func TestExecuteRequestDoesNotLeakKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key sk-test-secret", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	err := HTTPTransport{Client: server.Client()}.Execute(context.Background(), transportDraft(server.URL, ProtocolResponses, AuthRequirementBearerKey), "sk-test-secret", nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if strings.Contains(err.Error(), "sk-test-secret") {
		t.Fatalf("error leaked key: %q", err.Error())
	}
}

func TestExecuteRequestRequiresBearerKeyWhenNeeded(t *testing.T) {
	err := HTTPTransport{}.Execute(t.Context(), transportDraft("https://example.test", ProtocolResponses, AuthRequirementBearerKey), "", nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want missing bearer error")
	}
}
