package edge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fairy/app/core"
	"fairy/plugin"
	"fairy/runtime/config"
	"fairy/runtime/observability"
	"fairy/runtime/wasm"
)

func TestWebSearchStatusUsesRuntimeProfileBoundary(t *testing.T) {
	root := t.TempDir()
	if err := config.WriteWebSearchSettings(root, config.WebSearchSettings{SchemaVersion: 1, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAIRY_OPENSERP_URL", "https://environment.example")
	service := config.NewConfigService(root, nil)
	strict, err := webSearchStatusForRuntime(&core.Runtime{Config: service, RuntimeProfile: core.ProfileEndpointStrict})
	if err != nil {
		t.Fatal(err)
	}
	if strict.BaseURL == "https://environment.example" {
		t.Fatalf("strict status inherited environment origin: %#v", strict)
	}
	full, err := webSearchStatusForRuntime(&core.Runtime{Config: service, RuntimeProfile: core.ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	if full.BaseURL != "https://environment.example" {
		t.Fatalf("full status = %#v, want development environment origin", full)
	}
}

func TestSaveWebSearchUsesRuntimeProfileBoundary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FAIRY_OPENSERP_URL", "https://environment.example")
	rt := &core.Runtime{
		Config:         config.NewConfigService(root, nil),
		RuntimeProfile: core.ProfileEndpointStrict,
	}
	status, err := (&Management{runtime: &Runtime{core: rt}}).SaveWebSearch(WebSearchWrite{
		Enabled: false,
		BaseURL: "https://saved.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.Ready || status.BaseURL != "https://saved.example" {
		t.Fatalf("strict saved status = %#v", status)
	}
}

func TestEndpointStrictManagementRejectsLocalChatAndEmbeddingProviders(t *testing.T) {
	root := t.TempDir()
	rt := &core.Runtime{
		Config:         config.NewConfigService(root, config.NewTestSecretStore()),
		RuntimeProfile: core.ProfileEndpointStrict,
	}
	management := &Management{runtime: &Runtime{core: rt}}
	secret := "secret-must-not-appear"
	if _, err := management.SaveModel(ModelWrite{
		ModelConnectionInput: config.ModelConnectionInput{
			Protocol: "responses", Endpoint: "http://127.0.0.1:11434/v1", Model: "local-model",
			ContextWindowTokens: 8192, AuthMode: "bearer_key",
		},
		APIKey: secret,
	}); !errors.Is(err, config.ErrEndpointProviderLocal) || strings.Contains(err.Error(), secret) {
		t.Fatalf("SaveModel(local) error = %v, want scrubbed %v", err, config.ErrEndpointProviderLocal)
	}
	if _, err := management.SaveSemantic(SemanticWrite{
		Provider: config.SemanticEmbeddingProviderOpenAICompatible,
		Enabled:  true, Endpoint: "http://localhost:8080/v1", Model: "local-embedding", APIKey: secret,
	}); !errors.Is(err, config.ErrEndpointProviderLocal) || strings.Contains(err.Error(), secret) {
		t.Fatalf("SaveSemantic(local) error = %v, want scrubbed %v", err, config.ErrEndpointProviderLocal)
	}
	model, err := config.ReadModelConnectionStatus(root)
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := config.ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if model.Configured || semantic.Enabled || semantic.Provider != config.SemanticEmbeddingProviderNone {
		t.Fatalf("local provider attempt mutated config: model=%#v semantic=%#v", model, semantic)
	}
}

func TestManagementFailsClosedWithoutRuntime(t *testing.T) {
	var management *Management
	if _, err := management.Overview(t.Context()); !errors.Is(err, ErrManagementUnavailable) {
		t.Fatalf("Overview() error = %v, want %v", err, ErrManagementUnavailable)
	}
	if _, err := management.Plugins(); !errors.Is(err, ErrManagementUnavailable) {
		t.Fatalf("Plugins() error = %v, want %v", err, ErrManagementUnavailable)
	}
	if _, err := management.Metrics(t.Context()); !errors.Is(err, ErrObservabilityUnavailable) {
		t.Fatalf("Metrics() error = %v, want %v", err, ErrObservabilityUnavailable)
	}
}

func TestRuntimeManagementIsNilWithoutCore(t *testing.T) {
	var runtime *Runtime
	if runtime.Management() != nil {
		t.Fatal("nil runtime exposed management")
	}
	if _, err := runtime.PluginHost(); !errors.Is(err, ErrPluginHostUnavailable) {
		t.Fatalf("PluginHost() = %v, want %v", err, ErrPluginHostUnavailable)
	}
}

func TestManagementPluginsFailClosedUntilHostExists(t *testing.T) {
	runtime := &Runtime{}
	management := &Management{runtime: runtime}
	status, err := management.Plugins()
	if !errors.Is(err, ErrPluginHostUnavailable) {
		t.Fatalf("Plugins() = (%#v, %v), want %v", status, err, ErrPluginHostUnavailable)
	}
	if status.Ready {
		t.Fatal("Plugins() reported ready without a host")
	}
}

func TestManagementPluginsProjectsMetricsWithoutSecrets(t *testing.T) {
	metrics := observability.NewPluginMetrics()
	host, err := wasm.OpenWith(t.Context(), wasm.Observer{Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	metrics.Finish("echo-1", plugin.CodeCapabilityDenied, "trace-keep", false, false)
	metrics.Finish("echo-1", plugin.CodeBudgetExceeded, "trace-keep", false, false)
	status, err := (&Management{runtime: &Runtime{host: host}}).Plugins()
	if err != nil || !status.Ready || status.Metrics.CapabilityDenied != 1 || status.Metrics.BudgetExceeded != 1 {
		t.Fatalf("Plugins() = (%#v, %v)", status, err)
	}
	if len(status.Instances) != 1 || status.Instances[0].ID != "echo-1" || status.Instances[0].LastTraceID != "trace-keep" || status.Upgrades == nil {
		t.Fatalf("instance projection = %#v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(raw))
	for _, forbidden := range []string{"sk-live", "bearer ", "authorization", `"upgrades":null`, `"instances":null`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("plugin status contained %q: %s", forbidden, raw)
		}
	}
}

func TestModelWriteJSONDoesNotEchoAPIKeyOnStatus(t *testing.T) {
	secret := "sk-live-management-secret-value"
	write := ModelWrite{APIKey: secret}
	raw, err := json.Marshal(write)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), secret) {
		t.Fatal("write payload must carry the credential into the host method")
	}
	status := ModelStatus{Configured: true, Protocol: "openai_compatible_api", Model: "test-model", AuthMode: "bearer_key"}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, secret) || strings.Contains(text, "apiKey") || strings.Contains(text, "sk-live") {
		t.Fatalf("model status echoed credential: %s", text)
	}
}

func TestTurnRuntimeViewOmitsMetadataJSON(t *testing.T) {
	view := TurnRuntimeView{
		ConversationID: "c1",
		TurnID:         "t1",
		Events: []TurnRuntimeEvent{{
			Sequence: 1, EventType: "model", CreatedAtUnixMS: 1,
		}},
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"metadatajson", "authorization", "bearer", "apikey", "api_key"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("turn runtime view contained %q: %s", forbidden, encoded)
		}
	}
}

func TestClosedRuntimeDoesNotExposeManagement(t *testing.T) {
	var rt *Runtime
	if rt.Management() != nil || rt.Session() != nil || rt.Facade() != nil || rt.Core() != nil {
		t.Fatal("nil runtime exposed management composition")
	}
}
