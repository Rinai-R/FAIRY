package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/secret"
)

func validConnectionDocument() string {
	return `{"schema_version":1,"data":{"schema_version":3,"connection_id":"6a129284-6358-47b0-ad64-2a5907d36c91","protocol":"chat_completions","endpoint":"https://api.deepseek.com","model":"deepseek-v4-flash","context_window_tokens":1048576,"auth_mode":"bearer_key","capabilities":{"prompt_cache_key":false,"cached_tokens_usage":true,"explicit_breakpoints":false,"cache_retention":false,"websocket_continuation":false}}}`
}

func TestParseModelConnectionStatusRedactsSecretState(t *testing.T) {
	status, err := ParseModelConnectionStatus([]byte(validConnectionDocument()))
	if err != nil {
		t.Fatalf("ParseModelConnectionStatus() error = %v", err)
	}
	if !status.Configured {
		t.Fatal("Configured = false, want true")
	}
	if status.Protocol != "chat_completions" {
		t.Fatalf("Protocol = %q", status.Protocol)
	}
	if status.Endpoint != "https://api.deepseek.com" {
		t.Fatalf("Endpoint = %q", status.Endpoint)
	}
	if status.Model != "deepseek-v4-flash" {
		t.Fatalf("Model = %q", status.Model)
	}
	if status.ContextWindowTokens != 1048576 {
		t.Fatalf("ContextWindowTokens = %d", status.ContextWindowTokens)
	}
	if status.AuthMode != "bearer_key" {
		t.Fatalf("AuthMode = %q", status.AuthMode)
	}
	if !status.Capabilities.CachedTokensUsage {
		t.Fatal("CachedTokensUsage = false, want true")
	}
	if !status.SecretStorageMigrated {
		t.Fatal("SecretStorageMigrated = false, want true for Go secret ownership")
	}
}

func TestParseModelConnectionPreservesInternalConnectionID(t *testing.T) {
	connection, err := ParseModelConnection([]byte(validConnectionDocument()))
	if err != nil {
		t.Fatalf("ParseModelConnection() error = %v", err)
	}
	if connection.ConnectionID != "6a129284-6358-47b0-ad64-2a5907d36c91" {
		t.Fatalf("ConnectionID = %q", connection.ConnectionID)
	}
	if connection.Protocol != "chat_completions" || connection.Model != "deepseek-v4-flash" {
		t.Fatalf("connection = %#v", connection)
	}
}

func TestReadModelConnectionMissingIsExplicitError(t *testing.T) {
	_, err := ReadModelConnection(t.TempDir())
	if err == nil {
		t.Fatal("ReadModelConnection() error = nil, want explicit unconfigured error")
	}
}

func TestParseModelConnectionStatusRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(string) string
		wantErr string
	}{
		{
			name: "bad document schema",
			edit: func(source string) string {
				return strings.Replace(source, `"schema_version":1`, `"schema_version":2`, 1)
			},
			wantErr: "document schema_version",
		},
		{
			name: "bad connection schema",
			edit: func(source string) string {
				return strings.Replace(source, `"schema_version":3`, `"schema_version":4`, 1)
			},
			wantErr: "connection schema_version",
		},
		{
			name: "unsupported protocol",
			edit: func(source string) string {
				return strings.Replace(source, `"chat_completions"`, `"auto"`, 1)
			},
			wantErr: "protocol",
		},
		{
			name: "missing endpoint",
			edit: func(source string) string {
				return strings.Replace(source, `"https://api.deepseek.com"`, `""`, 1)
			},
			wantErr: "endpoint",
		},
		{
			name: "unsupported auth mode",
			edit: func(source string) string {
				return strings.Replace(source, `"bearer_key"`, `"api_key"`, 1)
			},
			wantErr: "auth_mode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseModelConnectionStatus([]byte(tt.edit(validConnectionDocument())))
			if err == nil {
				t.Fatal("ParseModelConnectionStatus() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestReadModelConnectionStatusMissingIsExplicitlyUnconfigured(t *testing.T) {
	status, err := ReadModelConnectionStatus(t.TempDir())
	if err != nil {
		t.Fatalf("ReadModelConnectionStatus() error = %v", err)
	}
	if status.Configured {
		t.Fatal("Configured = true, want false")
	}
}

func TestReadModelConnectionStatusFromEnvironment(t *testing.T) {
	root := os.Getenv("FAIRY_CONFIG_ROOT")
	if root == "" {
		t.Skip("FAIRY_CONFIG_ROOT is not set")
	}
	status, err := ReadModelConnectionStatus(root)
	if err != nil {
		t.Fatalf("ReadModelConnectionStatus(%q) error = %v", root, err)
	}
	if !status.Configured {
		t.Fatalf("Configured = false for %s", filepath.Join(root, modelConnectionPath))
	}
}

func TestSaveModelConnectionWritesConfigAndSecret(t *testing.T) {
	root := t.TempDir()
	apiKey := "sk-test-secret"
	status, err := SaveModelConnection(root, ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://api.deepseek.com",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "bearer_key",
	}, &apiKey, nil)
	if err != nil {
		t.Fatalf("SaveModelConnection() error = %v", err)
	}
	if !status.Configured || status.AuthMode != "bearer_key" || !status.SecretStorageMigrated {
		t.Fatalf("status = %#v", status)
	}
	connection, err := ReadModelConnection(root)
	if err != nil {
		t.Fatalf("ReadModelConnection() error = %v", err)
	}
	value, ok, err := secret.NewStore(filepath.Join(root, secret.RelativePath)).Load(connection.ConnectionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !ok || value.Expose() != "sk-test-secret" {
		t.Fatalf("secret = (%v, %q)", ok, value.Expose())
	}
}

func TestSaveModelConnectionPreservesExistingSecretWhenKeyOmitted(t *testing.T) {
	root := t.TempDir()
	apiKey := "sk-test-secret"
	if _, err := SaveModelConnection(root, ModelConnectionInput{Protocol: "chat_completions", Endpoint: "https://api.deepseek.com", Model: "deepseek-v4-flash", ContextWindowTokens: 1048576, AuthMode: "bearer_key"}, &apiKey, nil); err != nil {
		t.Fatalf("initial SaveModelConnection() error = %v", err)
	}
	if _, err := SaveModelConnection(root, ModelConnectionInput{Protocol: "responses", Endpoint: "https://api.deepseek.com", Model: "deepseek-v4-flash", ContextWindowTokens: 1048576, AuthMode: "bearer_key"}, nil, nil); err != nil {
		t.Fatalf("second SaveModelConnection() error = %v", err)
	}
}

func TestSaveModelConnectionNoAuthDeletesExistingSecret(t *testing.T) {
	root := t.TempDir()
	apiKey := "sk-test-secret"
	if _, err := SaveModelConnection(root, ModelConnectionInput{Protocol: "chat_completions", Endpoint: "https://api.deepseek.com", Model: "deepseek-v4-flash", ContextWindowTokens: 1048576, AuthMode: "bearer_key"}, &apiKey, nil); err != nil {
		t.Fatalf("initial SaveModelConnection() error = %v", err)
	}
	connection, err := ReadModelConnection(root)
	if err != nil {
		t.Fatalf("ReadModelConnection() error = %v", err)
	}
	if _, err := SaveModelConnection(root, ModelConnectionInput{Protocol: "chat_completions", Endpoint: "https://api.deepseek.com", Model: "deepseek-v4-flash", ContextWindowTokens: 1048576, AuthMode: "no_auth"}, nil, nil); err != nil {
		t.Fatalf("no_auth SaveModelConnection() error = %v", err)
	}
	_, ok, err := secret.NewStore(filepath.Join(root, secret.RelativePath)).Load(connection.ConnectionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if ok {
		t.Fatal("secret still exists after no_auth save")
	}
}

func TestClearModelConnectionDeletesConfigAndSecret(t *testing.T) {
	root := t.TempDir()
	apiKey := "sk-test-secret"
	if _, err := SaveModelConnection(root, ModelConnectionInput{Protocol: "chat_completions", Endpoint: "https://api.deepseek.com", Model: "deepseek-v4-flash", ContextWindowTokens: 1048576, AuthMode: "bearer_key"}, &apiKey, nil); err != nil {
		t.Fatalf("SaveModelConnection() error = %v", err)
	}
	cleared, err := ClearModelConnection(root, nil)
	if err != nil {
		t.Fatalf("ClearModelConnection() error = %v", err)
	}
	if !cleared {
		t.Fatal("ClearModelConnection() = false, want true")
	}
	status, err := ReadModelConnectionStatus(root)
	if err != nil {
		t.Fatalf("ReadModelConnectionStatus() error = %v", err)
	}
	if status.Configured {
		t.Fatalf("status = %#v", status)
	}
}
