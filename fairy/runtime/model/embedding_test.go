package model

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestEndpointSemanticEmbedderUsesSavedProviderWithoutEnvironmentProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data":   []map[string]any{{"index": 0, "object": "embedding", "embedding": testEmbeddingVector(3)}},
			"model":  "embedding-model",
			"object": "list",
		}); err != nil {
			t.Fatal(err)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	root := t.TempDir()
	secrets := config.NewTestSecretStore()
	key := "sk-semantic-strict"
	settings := config.SemanticEmbeddingSettings{
		Provider:   config.SemanticEmbeddingProviderOpenAICompatible,
		Enabled:    true,
		Endpoint:   endpointTestProviderURL(t, server, "/v1"),
		Model:      "embedding-model",
		Dimensions: config.SemanticEmbeddingDimensions,
	}
	if _, err := config.NewConfigService(root, secrets).SaveSemanticEmbeddingSettings(settings, &key); err != nil {
		t.Fatal(err)
	}
	settings, err := config.ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	service := NewEndpointModelService(root, secrets)
	service.endpointClient = endpointTestClientFactory(server)
	embedder, err := service.SemanticEmbedder(settings)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := embedder.EmbedContext(t.Context(), []string{"saved provider only"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 || len(vectors[0]) != config.SemanticEmbeddingDimensions || vectors[0][0] != 3 {
		t.Fatalf("vectors = %d x %d, first=%v", len(vectors), len(vectors[0]), vectors[0][0])
	}
}
