package config

import "testing"

func TestReaderModelConnectionAndWebSearch(t *testing.T) {
	root := t.TempDir()
	apiKey := "sk-test-secret"
	if _, err := SaveModelConnection(root, ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://api.deepseek.com",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "bearer_key",
	}, &apiKey, nil); err != nil {
		t.Fatalf("SaveModelConnection() error = %v", err)
	}
	reader := NewReader(root)
	connection, err := reader.ModelConnection()
	if err != nil {
		t.Fatalf("ModelConnection() error = %v", err)
	}
	if connection.Model != "deepseek-v4-flash" {
		t.Fatalf("Model = %q", connection.Model)
	}
	settings, err := reader.WebSearchSettings()
	if err != nil {
		t.Fatalf("WebSearchSettings() error = %v", err)
	}
	if !settings.Enabled {
		t.Fatal("WebSearchSettings().Enabled = false, want default true")
	}
	semanticSettings, err := reader.SemanticEmbeddingSettings()
	if err != nil {
		t.Fatalf("SemanticEmbeddingSettings() error = %v", err)
	}
	if !semanticSettings.Enabled || semanticSettings.Provider != SemanticEmbeddingProviderLocalBGE || semanticSettings.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("SemanticEmbeddingSettings() = %#v", semanticSettings)
	}
}
