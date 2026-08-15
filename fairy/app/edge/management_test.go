package edge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

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
	if err := runtime.PluginHost(); !errors.Is(err, ErrPluginHostUnavailable) {
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
