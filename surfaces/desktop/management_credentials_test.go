package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"fairy/app/core"
	"fairy/app/edge"
	"fairy/runtime/config"
	"fairy/runtime/observability"
	api "fairy/transport/web"
)

func TestManagementFrontendBoundPayloadsNeverEchoCredentials(t *testing.T) {
	secrets := []string{
		"sk-live-fixture-secret-value",
		"sf-semantic-fixture-secret",
		"search-provider-fixture-secret",
		"plugin-credential-fixture-secret",
		"seekdb-private-password",
		"qq-access-token-fixture",
	}
	modelSecret := secrets[0]
	semanticSecret := secrets[1]
	store := observability.NewLogStore(8)
	store.Append(observability.EntryInput{
		Level:   "error",
		Logger:  "companion",
		Message: "request Authorization: Bearer " + modelSecret,
		Fields: []observability.FieldInput{
			{Key: "apiKey", Value: modelSecret},
			{Key: "password", Value: secrets[4]},
			{Key: "access_token", Value: secrets[5]},
		},
	})
	host := scriptedManagement{
		overview: func(context.Context) (edge.Overview, error) {
			return edge.Overview{
				Bootstrap: core.BootstrapStatus{AppName: "FAIRY", CoreVersion: "dev"},
				Storage:   api.StorageStatus{Ready: false, Mode: "production", Storage: "seekdb", Error: "dial failed with [seekdb-credential]"},
				SecretKey: edge.SecretKeyStatus{Ready: true, Mode: "production"},
				Model:     edge.ModelStatus{Configured: true, Protocol: "openai_compatible_api", Model: "test", AuthMode: "bearer_key"},
				Semantic:  edge.SemanticStatus{Provider: "siliconflow", Enabled: true, Configured: true, CredentialConfigured: true},
				WebSearch: config.WebSearchStatus{Enabled: true, Ready: true},
			}, nil
		},
		saveModel: func(write edge.ModelWrite) (edge.ModelStatus, error) {
			if write.APIKey != modelSecret {
				t.Fatalf("model host received %q", write.APIKey)
			}
			return edge.ModelStatus{Configured: true, Protocol: write.Protocol, Model: write.Model, AuthMode: write.AuthMode}, nil
		},
		model: func() (edge.ModelStatus, error) {
			return edge.ModelStatus{Configured: true, Protocol: "openai_compatible_api", Model: "test", AuthMode: "bearer_key"}, nil
		},
		saveSemantic: func(write edge.SemanticWrite) (edge.SemanticStatus, error) {
			if write.APIKey != semanticSecret {
				t.Fatalf("semantic host received %q", write.APIKey)
			}
			return edge.SemanticStatus{Provider: write.Provider, Enabled: true, Configured: true, CredentialConfigured: true}, nil
		},
		semantic: func() (edge.SemanticStatus, error) {
			return edge.SemanticStatus{Provider: "siliconflow", Enabled: true, Configured: true, CredentialConfigured: true}, nil
		},
		qq: func() (edge.QQSettings, error) {
			return edge.QQSettings{SchemaVersion: 1, GroupAllowlist: []string{"123"}}, nil
		},
		saveQQ: func(settings edge.QQSettings) (edge.QQSettings, error) {
			return settings, nil
		},
		plugins: func() (edge.PluginStatus, error) {
			return edge.PluginStatus{}, edge.ErrPluginHostUnavailable
		},
		logs: func(filter edge.LogFilter) (edge.LogSnapshot, error) {
			return store.Query(filter), nil
		},
		trace: func(_ context.Context, traceID string) (edge.TraceDetail, error) {
			return edge.TraceDetail{
				TraceID: traceID, Status: "ok", DurationMS: 12,
				Spans: []observability.TraceSpan{{SpanID: "span-1", Operation: "model", Status: "ok", DurationMS: 12, Attributes: map[string]string{"status": "ok"}}},
			}, nil
		},
	}
	service := NewCoreService()
	service.edge = &fakeOwnedRuntime{host: host}

	modelStatus, err := service.SaveManagementModel(edge.ModelWrite{
		ModelConnectionInput: config.ModelConnectionInput{Protocol: "openai_compatible_api", Model: "test", AuthMode: "bearer_key"},
		APIKey:               modelSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	semanticStatus, err := service.SaveManagementSemantic(edge.SemanticWrite{Provider: "siliconflow", Enabled: true, APIKey: semanticSecret})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := service.ManagementOverview()
	if err != nil {
		t.Fatal(err)
	}
	qq, err := service.SaveManagementQQ(edge.QQSettings{SchemaVersion: 1, GroupAllowlist: []string{"123"}})
	if err != nil {
		t.Fatal(err)
	}
	logs, err := service.ManagementLogs()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.Entries) != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	trace, err := service.ManagementTrace("trace-1")
	if err != nil {
		t.Fatal(err)
	}
	_, pluginErr := service.ManagementPlugins()
	if !errors.Is(pluginErr, edge.ErrPluginHostUnavailable) {
		t.Fatalf("ManagementPlugins() error = %v", pluginErr)
	}

	payloads := []any{modelStatus, semanticStatus, overview, qq, logs, trace, pluginErr.Error()}
	for _, payload := range payloads {
		assertNoSecretPlaintext(t, payload, secrets...)
	}
	if strings.Contains(logs.Entries[0].Message, modelSecret) {
		t.Fatalf("log message was not redacted: %#v", logs.Entries[0])
	}
	for _, field := range logs.Entries[0].Fields {
		if field.Value != observability.RedactedValue {
			t.Fatalf("log field %q = %q, want redacted", field.Key, field.Value)
		}
	}
}

func TestManagementSaveErrorsDoNotEchoSubmittedCredentials(t *testing.T) {
	secret := "sk-live-rejected-secret"
	host := scriptedManagement{
		saveModel: func(edge.ModelWrite) (edge.ModelStatus, error) {
			return edge.ModelStatus{}, errors.New("model credential is required")
		},
		saveSemantic: func(edge.SemanticWrite) (edge.SemanticStatus, error) {
			return edge.SemanticStatus{}, errors.New("semantic credential is required")
		},
	}
	service := NewCoreService()
	service.edge = &fakeOwnedRuntime{host: host}
	_, modelErr := service.SaveManagementModel(edge.ModelWrite{APIKey: secret})
	if modelErr == nil {
		t.Fatal("SaveManagementModel() error = nil")
	}
	_, semanticErr := service.SaveManagementSemantic(edge.SemanticWrite{APIKey: secret})
	if semanticErr == nil {
		t.Fatal("SaveManagementSemantic() error = nil")
	}
	assertNoSecretPlaintext(t, modelErr.Error(), secret)
	assertNoSecretPlaintext(t, semanticErr.Error(), secret)
}

func assertNoSecretPlaintext(t *testing.T, value any, secrets ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Fatalf("payload contained %q: %s", secret, text)
		}
	}
}

func TestDesktopFrontendProductionSourcesOmitCredentialPlaintext(t *testing.T) {
	tokens := []string{
		"FAIRY_API_TOKEN",
		"127.0.0.1:8787",
		"fairy.apiToken",
		"postgres://",
		"mysql://",
		"sk-live-",
		"qq-access-token",
		"seekdb-private-password",
	}
	err := walkDesktopProductionArtifacts(func(path string, content []byte) error {
		if !strings.Contains(path, string(filepath.Separator)+"frontend"+string(filepath.Separator)) {
			return nil
		}
		if strings.Contains(filepath.Base(path), ".test.") {
			return nil
		}
		text := string(content)
		lower := strings.ToLower(text)
		for _, token := range tokens {
			if strings.Contains(text, token) {
				t.Errorf("%s contains forbidden credential marker %q", path, token)
			}
		}
		if strings.Contains(lower, "pmhq") {
			t.Errorf("%s contains PMHQ", path)
		}
		if strings.Contains(text, "Authorization") {
			t.Errorf("%s contains Authorization", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
