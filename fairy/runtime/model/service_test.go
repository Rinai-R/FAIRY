package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/runtime/config"
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

type overflowingStreamTransport struct {
	events      []StreamEvent
	sawCanceled bool
}

type lateAfterCancelTransport struct{}

func (lateAfterCancelTransport) Execute(ctx context.Context, _ RequestDraft, _ string, onEvent func(StreamEvent)) error {
	onEvent(StreamEvent{Type: "text_delta", Data: "first"})
	<-ctx.Done()
	onEvent(StreamEvent{Type: "completed", FinishReason: "stop"})
	return ctx.Err()
}

func (t *overflowingStreamTransport) Execute(ctx context.Context, _ RequestDraft, _ string, onEvent func(StreamEvent)) error {
	for _, event := range t.events {
		onEvent(event)
		if ctx.Err() != nil {
			t.sawCanceled = true
		}
	}
	return nil
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

func saveModelSecret(t *testing.T, raw string) *config.SecretStore {
	t.Helper()
	store := config.NewTestSecretStore()
	value, err := config.NewSecretValue(raw)
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

func TestNormalizeCacheInputUpgradesLegacyIdentity(t *testing.T) {
	request := modelServiceRequest()
	request.Shape.PromptCacheKey = "fairy:conversation-1:respond"
	normalized := normalizeCacheInput(request)
	if normalized.CacheInput == nil || normalized.CacheInput.Seed != request.Shape.PromptCacheKey || normalized.CacheInput.StablePromptHash == "" {
		t.Fatalf("cache input = %#v", normalized.CacheInput)
	}
	request.Input[0].Content = "different dynamic dialogue"
	second := normalizeCacheInput(request)
	firstKey, err := BuildPromptCacheKey(*normalized.CacheInput)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := BuildPromptCacheKey(*second.CacheInput)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey != secondKey {
		t.Fatalf("dynamic input changed cache identity: %q != %q", firstKey, secondKey)
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

func TestModelServiceReadsSynchronousConfigUpdatesWithoutRestart(t *testing.T) {
	root := t.TempDir()
	serviceConfig := config.NewConfigService(root, config.NewTestSecretStore())
	if _, err := serviceConfig.SaveModelConnection(config.ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://first.example",
		Model:               "first-model",
		ContextWindowTokens: 8192,
		AuthMode:            "no_auth",
	}, nil); err != nil {
		t.Fatalf("initial SaveModelConnection() error = %v", err)
	}
	service := NewModelService(root, nil)
	firstRequest := modelServiceRequest()
	firstRequest.Shape.Model = "first-model"
	first, err := service.BuildRequestDraft(firstRequest)
	if err != nil {
		t.Fatalf("first BuildRequestDraft() error = %v", err)
	}
	if !strings.Contains(first.BodyJSON, `"model":"first-model"`) {
		t.Fatalf("first draft model missing: %s", first.BodyJSON)
	}

	if _, err := serviceConfig.SaveModelConnection(config.ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://second.example",
		Model:               "second-model",
		ContextWindowTokens: 16384,
		AuthMode:            "no_auth",
	}, nil); err != nil {
		t.Fatalf("updated SaveModelConnection() error = %v", err)
	}
	secondRequest := modelServiceRequest()
	secondRequest.Shape.Model = "second-model"
	second, err := service.BuildRequestDraft(secondRequest)
	if err != nil {
		t.Fatalf("second BuildRequestDraft() error = %v", err)
	}
	if second.URL != "https://second.example/chat/completions" || !strings.Contains(second.BodyJSON, `"model":"second-model"`) {
		t.Fatalf("updated draft did not observe durable config: url=%q body=%s", second.URL, second.BodyJSON)
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
	if events[2].Type != "completed" || events[2].FinishReason != "stop" {
		t.Fatalf("completed event = %#v", events[2])
	}
}

func TestEndpointModelServiceUsesOnlySavedAuthorityAndIgnoresEnvironmentProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-saved-endpoint-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "" {
			t.Fatalf("X-Api-Key = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"端\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(server.Close)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	t.Setenv("OPENAI_API_KEY", "sk-environment-openai-private")
	t.Setenv("OPENAI_BASE_URL", "https://environment-openai.example/v1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-environment-anthropic-private")
	t.Setenv("CODEBUDDY_API_KEY", "sk-environment-codebuddy-private")
	t.Setenv("CODEBUDDY_BASE_URL", "https://environment-codebuddy.example")

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", endpointTestProviderURL(t, server, ""), "bearer_key")
	service := NewEndpointModelService(root, saveModelSecret(t, "sk-saved-endpoint-secret"))
	service.transport = SDKTransport{endpointStrict: true, endpointClient: endpointTestClientFactory(server)}
	events, err := service.ExecuteRequest(modelServiceRequest())
	if err != nil {
		t.Fatalf("ExecuteRequest() error = %v", err)
	}
	if len(events) != 2 || events[0].Data != "端" || events[1].Type != "completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestEndpointModelServiceRejectsProviderRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", endpointTestProviderURL(t, server, ""), "no_auth")
	service := NewEndpointModelService(root, nil)
	service.transport = SDKTransport{endpointStrict: true, endpointClient: endpointTestClientFactory(server)}
	_, err := service.ExecuteRequest(modelServiceRequest())
	if err == nil || !strings.Contains(err.Error(), ErrProviderRedirectDenied.Error()) {
		t.Fatalf("ExecuteRequest() error = %v, want redirect denied", err)
	}
	if redirected.Load() {
		t.Fatal("endpoint model service followed provider redirect")
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

func TestModelServiceCancellationDropsLateProviderCompletion(t *testing.T) {
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", "http://model.test", "no_auth")
	service := NewModelServiceWithTransport(root, lateAfterCancelTransport{}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := make([]StreamEvent, 0, 1)

	err := service.ExecuteRequestContextStream(ctx, modelServiceRequest(), func(event StreamEvent) {
		events = append(events, event)
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteRequestContextStream() error = %v, want context.Canceled", err)
	}
	if len(events) != 1 || events[0].Type != "text_delta" || events[0].Data != "first" {
		t.Fatalf("events after cancellation = %#v", events)
	}
}

func TestModelServiceBoundsStreamEventsAndDropsCallbacksAfterCancellation(t *testing.T) {
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", "http://model.test", "no_auth")
	events := make([]StreamEvent, MaxModelStreamEvents+2)
	for index := range events {
		events[index] = StreamEvent{Type: "text_delta", Data: "x"}
	}
	events[len(events)-1] = StreamEvent{Type: "completed"}
	transport := &overflowingStreamTransport{events: events}
	service := NewModelServiceWithTransport(root, transport, nil)
	delivered := 0
	completed := false
	err := service.ExecuteRequestContextStream(t.Context(), modelServiceRequest(), func(event StreamEvent) {
		delivered++
		completed = completed || event.Type == "completed"
	})
	if !errors.Is(err, ErrModelStreamCapacity) {
		t.Fatalf("Execute error = %v, want ErrModelStreamCapacity", err)
	}
	if delivered != MaxModelStreamEvents || completed {
		t.Fatalf("delivered=%d completed=%v", delivered, completed)
	}
	if !transport.sawCanceled {
		t.Fatal("transport context was not canceled at capacity")
	}
}

func TestModelServiceBoundsStreamPayloadBeforeConsumer(t *testing.T) {
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", "http://model.test", "no_auth")
	transport := &overflowingStreamTransport{events: []StreamEvent{{
		Type: "text_delta", Data: strings.Repeat("x", MaxModelStreamPayloadBytes+1),
	}}}
	service := NewModelServiceWithTransport(root, transport, nil)
	delivered := 0
	err := service.ExecuteRequestContextStream(t.Context(), modelServiceRequest(), func(StreamEvent) {
		delivered++
	})
	if !errors.Is(err, ErrModelStreamCapacity) || delivered != 0 {
		t.Fatalf("error=%v delivered=%d", err, delivered)
	}
	if !transport.sawCanceled {
		t.Fatal("transport context was not canceled for payload capacity")
	}
}

func TestModelServiceRejectsOversizedFunctionCallEventBeforeConsumer(t *testing.T) {
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", "http://model.test", "no_auth")
	calls := make([]FunctionCall, MaxModelFunctionCalls+1)
	transport := &overflowingStreamTransport{events: []StreamEvent{{
		Type: "function_calls", FunctionCalls: calls,
	}}}
	service := NewModelServiceWithTransport(root, transport, nil)
	delivered := 0
	err := service.ExecuteRequestContextStream(t.Context(), modelServiceRequest(), func(StreamEvent) {
		delivered++
	})
	if !errors.Is(err, ErrModelStreamCapacity) || delivered != 0 {
		t.Fatalf("error=%v delivered=%d", err, delivered)
	}
}
