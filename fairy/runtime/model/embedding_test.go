package model

import (
	"strings"
	"testing"

	"fairy/runtime/config"
)

func TestSemanticEmbedderUsesIndependentSettingsAndCredential(t *testing.T) {
	root := t.TempDir()
	secrets := config.NewTestSecretStore()
	chatKey := "sk-chat-only"
	if _, err := config.SaveModelConnection(root, config.ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://chat.example.test/v1",
		Model:               "chat-model",
		ContextWindowTokens: 8192,
		AuthMode:            "bearer_key",
	}, &chatKey, secrets); err != nil {
		t.Fatalf("SaveModelConnection() error = %v", err)
	}

	semanticKey := "sk-semantic-only"
	configService := config.NewConfigService(root, secrets)
	if _, err := configService.SaveSemanticEmbeddingSettings(config.SiliconFlowSemanticEmbeddingDefaults(), &semanticKey); err != nil {
		t.Fatalf("SaveSemanticEmbeddingSettings() error = %v", err)
	}
	settings, err := config.ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}

	embedder, err := NewModelService(root, secrets).SemanticEmbedder(settings)
	if err != nil {
		t.Fatalf("SemanticEmbedder() error = %v", err)
	}
	if !embedder.Ready() {
		t.Fatal("Ready() = false")
	}
	if embedder.baseURL != "https://api.siliconflow.cn/v1/" {
		t.Fatalf("baseURL = %q", embedder.baseURL)
	}
	if embedder.bearerKey != semanticKey || embedder.bearerKey == chatKey {
		t.Fatal("semantic embedder did not use its independent credential")
	}
	if embedder.model != "BAAI/bge-m3" || !strings.HasPrefix(embedder.ModelID(), "embedding-space-v1:") || embedder.Dims() != config.SemanticEmbeddingDimensions {
		t.Fatalf("model contract = (%q, %q, %d)", embedder.model, embedder.ModelID(), embedder.Dims())
	}
}

func TestSemanticEmbedderDoesNotFallBackToChatCredential(t *testing.T) {
	root := t.TempDir()
	secrets := config.NewTestSecretStore()
	chatKey := "sk-chat-only"
	if _, err := config.SaveModelConnection(root, config.ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://chat.example.test/v1",
		Model:               "chat-model",
		ContextWindowTokens: 8192,
		AuthMode:            "bearer_key",
	}, &chatKey, secrets); err != nil {
		t.Fatalf("SaveModelConnection() error = %v", err)
	}
	settings := config.SiliconFlowSemanticEmbeddingDefaults()
	settings.ConnectionID = "semantic_embedding.missing"

	_, err := NewModelService(root, secrets).SemanticEmbedder(settings)
	if err == nil || !strings.Contains(err.Error(), "semantic embedding credential is not configured") {
		t.Fatalf("SemanticEmbedder() error = %v", err)
	}
	if strings.Contains(err.Error(), chatKey) {
		t.Fatalf("SemanticEmbedder() leaked chat credential: %v", err)
	}
}
