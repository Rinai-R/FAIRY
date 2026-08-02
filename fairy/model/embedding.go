package model

import (
	"context"
	"errors"
	"fmt"

	"fairy/config"
)

// ExecuteRequest runs a prepared draft via the default SDK transport.
func ExecuteRequest(ctx context.Context, draft RequestDraft, bearerKey string, onEvent func(StreamEvent)) error {
	return SDKTransport{}.Execute(ctx, draft, bearerKey, onEvent)
}

func (s *ModelService) SemanticEmbedder(settings config.SemanticEmbeddingSettings) (*APIEmbedder, error) {
	if !settings.Enabled || settings.LegacyReason != "" {
		return nil, errors.New("semantic embedding provider is not enabled")
	}
	if settings.Provider != config.SemanticEmbeddingProviderSiliconFlow && settings.Provider != config.SemanticEmbeddingProviderOpenAICompatible {
		return nil, errors.New("semantic embedding API provider is not selected")
	}
	if settings.ConnectionID == "" {
		return nil, errors.New("semantic embedding credential is not configured")
	}
	store, err := resolveSecretStore(s.root, s.secrets)
	if err != nil {
		return nil, err
	}
	credential, ok, err := store.Load(settings.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("loading semantic embedding credential: %w", err)
	}
	if !ok {
		return nil, errors.New("semantic embedding credential is not configured")
	}
	return NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   settings.Endpoint,
		AuthMode:   "bearer_key",
		BearerKey:  credential.Expose(),
		Model:      settings.Model,
		Dimensions: settings.Dimensions,
	})
}
