package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fairy/secret"
)

type blockingStreamTransport struct {
	release <-chan struct{}
}

func (t blockingStreamTransport) Execute(ctx context.Context, _ RequestDraft, _ string, onEvent func(StreamEvent)) error {
	onEvent(StreamEvent{Type: "text_delta", Data: "first"})
	select {
	case <-t.release:
		onEvent(StreamEvent{Type: "completed"})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeModelConnection(t *testing.T, root string, protocol string) {
	writeModelConnectionWithEndpoint(t, root, protocol, "https://api.deepseek.com", "bearer_key")
}

func writeModelConnectionWithEndpoint(t *testing.T, root string, protocol string, endpoint string, authMode string) {
	t.Helper()
	dir := filepath.Join(root, "model")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	document := "{\"schema_version\":1,\"data\":{\"schema_version\":3,\"connection_id\":\"6a129284-6358-47b0-ad64-2a5907d36c91\",\"protocol\":\"" + protocol + "\",\"endpoint\":\"" + endpoint + "\",\"model\":\"deepseek-v4-flash\",\"context_window_tokens\":1048576,\"auth_mode\":\"" + authMode + "\",\"capabilities\":{\"prompt_cache_key\":false,\"cached_tokens_usage\":true,\"explicit_breakpoints\":false,\"cache_retention\":false,\"websocket_continuation\":false}}}"
	if err := os.WriteFile(filepath.Join(dir, "connection.json"), []byte(document), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func saveModelSecret(t *testing.T, raw string) *secret.Store {
	t.Helper()
	store := secret.NewTestStore()
	value, err := secret.NewValue(raw)
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	if err := store.Save("6a129284-6358-47b0-ad64-2a5907d36c91", value); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return store
}

func modelServiceRequest() CompiledPromptRequest {
	return CompiledPromptRequest{
		Shape: ModelRequestShape{
			Lane:            PromptLaneRespond,
			Model:           "deepseek-v4-flash",
			Instructions:    "stable instructions",
			MaxOutputTokens: 160,
		},
		Input: []PromptItem{
			{Type: PromptItemUserMessage, Content: "你好"},
		},
	}
}

func TestModelServiceBuildRequestDraftUsesStoredConnection(t *testing.T) {
	root := t.TempDir()
	writeModelConnection(t, root, "chat_completions")
	service := NewModelService(root, nil)

	draft, err := service.BuildRequestDraft(modelServiceRequest())
	if err != nil {
		t.Fatalf("BuildRequestDraft() error = %v", err)
	}
	if draft.Protocol != ProtocolChatCompletions {
		t.Fatalf("Protocol = %q", draft.Protocol)
	}
	if draft.URL != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("URL = %q", draft.URL)
	}
	if !strings.Contains(draft.BodyJSON, "\"response_format\":{\"type\":\"json_object\"}") {
		t.Fatalf("BodyJSON missing JSON response format: %s", draft.BodyJSON)
	}
	if strings.Contains(draft.BodyJSON, "sk-") || strings.Contains(strings.ToLower(draft.BodyJSON), "authorization") {
		t.Fatalf("BodyJSON leaked secret-shaped data: %s", draft.BodyJSON)
	}
}

func TestModelServiceBuildRequestDraftFailsWhenUnconfigured(t *testing.T) {
	service := NewModelService(t.TempDir(), nil)
	_, err := service.BuildRequestDraft(modelServiceRequest())
	if err == nil {
		t.Fatal("BuildRequestDraft() error = nil, want unconfigured error")
	}
}

func TestModelServiceExecuteRequestUsesStoredSecretWithoutReturningIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-service-secret" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "bearer_key")
	secrets := saveModelSecret(t, "sk-service-secret")
	service := NewModelServiceWithTransport(root, SDKTransport{HTTPClient: server.Client()}, secrets)

	events, err := service.ExecuteRequest(modelServiceRequest())
	if err != nil {
		t.Fatalf("ExecuteRequest() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != "text_delta" || events[0].Data != "你" {
		t.Fatalf("text event = %#v", events[0])
	}
	if events[1].Type != "usage" || events[1].Usage.PromptTokens != 3 || events[1].Usage.CompletionTokens != 1 {
		t.Fatalf("usage event = %#v", events[1])
	}
	if events[2].Type != "completed" {
		t.Fatalf("completed event = %#v", events[2])
	}
}

func TestModelServiceExecuteRequestFailsWithoutStoredSecret(t *testing.T) {
	root := t.TempDir()
	writeModelConnection(t, root, "chat_completions")
	service := NewModelService(root, nil)

	_, err := service.ExecuteRequest(modelServiceRequest())
	if err == nil {
		t.Fatal("ExecuteRequest() error = nil, want missing credential error")
	}
	if strings.Contains(err.Error(), "sk-") || strings.Contains(strings.ToLower(err.Error()), "authorization") {
		t.Fatalf("ExecuteRequest() leaked secret-shaped error: %v", err)
	}
}

func TestModelServiceExecuteRequestOmitsSecretForNoAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	service := NewModelServiceWithTransport(root, SDKTransport{HTTPClient: server.Client()}, nil)

	events, err := service.ExecuteRequest(modelServiceRequest())
	if err != nil {
		t.Fatalf("ExecuteRequest() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != "completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestModelServiceExecuteRequestContextStreamDeliversBeforeReturn(t *testing.T) {
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", "http://model.test", "no_auth")
	release := make(chan struct{})
	service := NewModelServiceWithTransport(root, blockingStreamTransport{release: release}, nil)
	events := make(chan StreamEvent, 2)
	done := make(chan error, 1)

	go func() {
		done <- service.ExecuteRequestContextStream(context.Background(), modelServiceRequest(), func(event StreamEvent) {
			events <- event
		})
	}()

	select {
	case event := <-events:
		if event.Type != "text_delta" || event.Data != "first" {
			t.Fatalf("first event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("stream callback was not called before request completion")
	}
	select {
	case err := <-done:
		t.Fatalf("stream returned before transport release: %v", err)
	default:
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("ExecuteRequestContextStream() error = %v", err)
	}
	if event := <-events; event.Type != "completed" {
		t.Fatalf("last event = %#v", event)
	}
}
