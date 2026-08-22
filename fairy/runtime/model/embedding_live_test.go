//go:build live

package model

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"fairy/runtime/config"
)

func TestLiveEndpointSemanticEmbeddingUsesExplicitThirdPartyProvider(t *testing.T) {
	provider := strings.TrimSpace(os.Getenv("FAIRY_EMBEDDING_TEST_PROVIDER"))
	if provider == "" {
		provider = config.SemanticEmbeddingProviderOpenAICompatible
	}
	endpoint := strings.TrimSpace(os.Getenv("FAIRY_EMBEDDING_TEST_BASE_URL"))
	modelName := strings.TrimSpace(os.Getenv("FAIRY_EMBEDDING_TEST_MODEL"))
	apiKey := strings.TrimSpace(os.Getenv("FAIRY_EMBEDDING_TEST_API_KEY"))
	if endpoint == "" && modelName == "" && apiKey == "" {
		t.Skip("no explicit third-party live embedding credential")
	}
	if endpoint == "" || modelName == "" || apiKey == "" {
		t.Fatal("live embedding smoke requires FAIRY_EMBEDDING_TEST_BASE_URL, FAIRY_EMBEDDING_TEST_MODEL, and FAIRY_EMBEDDING_TEST_API_KEY together")
	}
	if err := config.ValidateEndpointStrictProviderURL(endpoint); err != nil {
		t.Fatalf("live embedding endpoint is not a third-party endpoint-strict provider: %v", err)
	}

	root := t.TempDir()
	secrets := config.NewTestSecretStore()
	settings := config.SemanticEmbeddingSettings{
		Provider:   provider,
		Enabled:    true,
		Endpoint:   endpoint,
		Model:      modelName,
		Dimensions: config.SemanticEmbeddingDimensions,
	}
	if _, err := config.NewConfigService(root, secrets).SaveSemanticEmbeddingSettings(settings, &apiKey); err != nil {
		t.Fatalf("save explicit third-party embedding settings: %v", err)
	}
	settings, err := config.ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	embedder, err := NewEndpointModelService(root, secrets).SemanticEmbedder(settings)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	vectors, err := embedder.EmbedContext(ctx, []string{"FAIRY 第三方语义向量验收 A", "FAIRY third-party embedding smoke B"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 {
		t.Fatalf("embedding vector count = %d, want 2", len(vectors))
	}
	for vectorIndex, vector := range vectors {
		if len(vector) != config.SemanticEmbeddingDimensions {
			t.Fatalf("embedding vector %d dimensions = %d, want %d", vectorIndex, len(vector), config.SemanticEmbeddingDimensions)
		}
		for dimension, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				t.Fatalf("embedding vector %d dimension %d is non-finite", vectorIndex, dimension)
			}
		}
	}
	if embedder.ModelID() == "" {
		t.Fatal("embedding space identity is empty")
	}
	t.Logf("third-party embedding provider=%s model=%s vectors=%d dimensions=%d", provider, modelName, len(vectors), embedder.Dims())
}
