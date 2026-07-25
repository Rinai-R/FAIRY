package model

import (
	"context"
	"errors"
	"fmt"

	"fairy/config"
	embeddingadapter "fairy/internal/adapters/model/embedding"
	openaiadapter "fairy/internal/adapters/model/openai"
	domainmodel "fairy/internal/domain/model"
)

type (
	Protocol        = domainmodel.Protocol
	AuthRequirement = domainmodel.AuthRequirement
	RequestDraft    = domainmodel.RequestDraft
	Usage           = domainmodel.Usage
	StreamEvent     = domainmodel.StreamEvent
	FunctionCall    = domainmodel.FunctionCall
	Transport       = domainmodel.Transport
	SDKTransport    = openaiadapter.SDKTransport

	APIEmbeddingOptions = embeddingadapter.APIEmbeddingOptions
	APIEmbedder         = embeddingadapter.APIEmbedder
)

const (
	ProtocolResponses        = domainmodel.ProtocolResponses
	ProtocolChatCompletions  = domainmodel.ProtocolChatCompletions
	AuthRequirementBearerKey = domainmodel.AuthRequirementBearerKey
	AuthRequirementNone      = domainmodel.AuthRequirementNone
)

var NewAPIEmbedder = embeddingadapter.NewAPIEmbedder

// ExecuteRequest runs a prepared draft via the default SDK transport.
func ExecuteRequest(ctx context.Context, draft RequestDraft, bearerKey string, onEvent func(StreamEvent)) error {
	return SDKTransport{}.Execute(ctx, draft, bearerKey, onEvent)
}

func scrubSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return openaiadapter.ScrubSecret(err, secret)
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
