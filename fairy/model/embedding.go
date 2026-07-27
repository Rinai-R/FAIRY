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

func (s *ModelService) SemanticAPIEmbedder(settings config.SemanticEmbeddingSettings) (*APIEmbedder, error) {
	if settings.Provider != config.SemanticEmbeddingProviderOpenAICompatible {
		return nil, errors.New("semantic embedding API provider is not selected")
	}
	connection, err := config.ReadModelConnection(s.root)
	if err != nil {
		return nil, fmt.Errorf("reading model connection: %w", err)
	}
	bearerKey, err := s.bearerCredential(connection)
	if err != nil {
		return nil, err
	}
	return NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   connection.Endpoint,
		AuthMode:   connection.AuthMode,
		BearerKey:  bearerKey,
		Model:      settings.Model,
		Dimensions: settings.Dimensions,
	})
}
