package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigServiceSaveAndClearModelConnection(t *testing.T) {
	service := NewConfigService(t.TempDir(), NewTestSecretStore())
	apiKey := "sk-test-secret"
	status, err := service.SaveModelConnection(ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://api.deepseek.com",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "bearer_key",
	}, &apiKey)
	if err != nil {
		t.Fatalf("SaveModelConnection() error = %v", err)
	}
	if !status.Configured || status.AuthMode != "bearer_key" {
		t.Fatalf("status = %#v", status)
	}
	status, err = service.ClearModelConnection()
	if err != nil {
		t.Fatalf("ClearModelConnection() error = %v", err)
	}
	if status.Configured {
		t.Fatalf("status = %#v", status)
	}
}

func TestConfigServiceModelReadinessRequiresOnlySavedCredential(t *testing.T) {
	root := t.TempDir()
	secrets := NewTestSecretStore()
	service := NewConfigService(root, secrets)
	status, err := service.ModelStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.Reason != "model_connection_required" {
		t.Fatalf("unconfigured status = %#v", status)
	}
	key := "sk-model-ready"
	status, err = service.SaveModelConnection(ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://provider.example.test/v1",
		Model:               "chat-model",
		ContextWindowTokens: 8192,
		AuthMode:            "bearer_key",
	}, &key)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || !status.CredentialConfigured || status.Reason != "" {
		t.Fatalf("ready status = %#v", status)
	}
	connection, err := ReadModelConnection(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Delete(connection.ConnectionID); err != nil {
		t.Fatal(err)
	}
	status, err = service.ModelStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.CredentialConfigured || status.Reason != "model_credential_required" {
		t.Fatalf("missing-credential status = %#v", status)
	}
}

func TestProviderStatusProjectionExcludesCredentialsAndEnvironmentOverrides(t *testing.T) {
	root := t.TempDir()
	secrets := NewTestSecretStore()
	service := NewConfigService(root, secrets)
	modelKey := "sk-saved-chat-private"
	semanticKey := "sk-saved-embedding-private"
	for key, value := range map[string]string{
		"OPENAI_API_KEY":        "sk-environment-chat-private",
		"OPENAI_BASE_URL":       "https://environment-model.example",
		"ANTHROPIC_API_KEY":     "sk-environment-anthropic-private",
		"CODEBUDDY_API_KEY":     "sk-environment-codebuddy-private",
		"CODEBUDDY_BASE_URL":    "https://environment-codebuddy.example",
		"HTTP_PROXY":            "http://environment-proxy.example:8080",
		"HTTPS_PROXY":           "http://environment-proxy.example:8080",
		"FAIRY_CORE_ENDPOINT":   "https://environment-core.example",
		"FAIRY_CORE_TOKEN":      "environment-core-private",
		"FAIRY_QQ_ONEBOT_TOKEN": "environment-qq-private",
	} {
		t.Setenv(key, value)
	}

	modelStatus, err := service.SaveModelConnection(ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://saved-model.example/v1",
		Model:               "saved-chat-model",
		ContextWindowTokens: 8192,
		AuthMode:            "bearer_key",
	}, &modelKey)
	if err != nil {
		t.Fatal(err)
	}
	semanticStatus, err := service.SaveSemanticEmbeddingSettings(SemanticEmbeddingSettings{
		Provider:   SemanticEmbeddingProviderOpenAICompatible,
		Enabled:    true,
		Endpoint:   "https://saved-embedding.example/v1",
		Model:      "saved-embedding-model",
		Dimensions: SemanticEmbeddingDimensions,
	}, &semanticKey)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(struct {
		Model    ModelConnectionStatus   `json:"model"`
		Semantic SemanticEmbeddingStatus `json:"semantic"`
	}{Model: modelStatus, Semantic: semanticStatus})
	if err != nil {
		t.Fatal(err)
	}
	projection := string(raw)
	for _, forbidden := range []string{
		modelKey, semanticKey,
		"sk-environment-chat-private", "environment-model.example", "environment-proxy.example",
		"sk-environment-anthropic-private", "sk-environment-codebuddy-private", "environment-codebuddy.example",
		"environment-core.example", "environment-core-private", "environment-qq-private",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("provider status projection leaked environment or credential %q: %s", forbidden, projection)
		}
	}
	if !strings.Contains(projection, "saved-model.example") || !strings.Contains(projection, "saved-embedding.example") {
		t.Fatalf("provider status projection lost saved authority identity: %s", projection)
	}
}
