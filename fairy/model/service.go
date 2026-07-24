package model

import (
	"context"
	"fmt"

	"fairy/config"
	"fairy/secret"
)

type ModelService struct {
	root      string
	transport Transport
	secrets   *secret.Store
}

func NewModelService(root string, secrets *secret.Store) *ModelService {
	return &ModelService{root: root, transport: SDKTransport{}, secrets: secrets}
}

func NewModelServiceWithTransport(root string, transport Transport, secrets *secret.Store) *ModelService {
	if transport == nil {
		transport = SDKTransport{}
	}
	return &ModelService{root: root, transport: transport, secrets: secrets}
}

func (s *ModelService) BuildRequestDraft(request CompiledPromptRequest) (RequestDraft, error) {
	status, err := config.ReadModelConnectionStatus(s.root)
	if err != nil {
		return RequestDraft{}, fmt.Errorf("reading model connection status: %w", err)
	}
	connection, err := ConnectionFromStatus(status)
	if err != nil {
		return RequestDraft{}, err
	}
	draft, err := BuildRequestDraft(connection, request)
	if err != nil {
		return RequestDraft{}, fmt.Errorf("building model request draft: %w", err)
	}
	return draft, nil
}

func (s *ModelService) ExecuteRequest(request CompiledPromptRequest) ([]StreamEvent, error) {
	return s.ExecuteRequestContext(context.Background(), request)
}

func (s *ModelService) ExecuteRequestContext(ctx context.Context, request CompiledPromptRequest) ([]StreamEvent, error) {
	events := make([]StreamEvent, 0)
	if err := s.ExecuteRequestContextStream(ctx, request, func(event StreamEvent) {
		events = append(events, event)
	}); err != nil {
		return nil, err
	}
	return events, nil
}

// ExecuteRequestContextStream executes a compiled request and invokes onEvent
// synchronously for each provider event, preserving provider order.
func (s *ModelService) ExecuteRequestContextStream(ctx context.Context, request CompiledPromptRequest, onEvent func(StreamEvent)) error {
	if ctx == nil {
		return fmt.Errorf("model request context is required")
	}
	if onEvent == nil {
		return fmt.Errorf("model stream callback is required")
	}
	connectionConfig, err := config.ReadModelConnection(s.root)
	if err != nil {
		return fmt.Errorf("reading model connection: %w", err)
	}
	connection, err := ConnectionFromConfig(connectionConfig)
	if err != nil {
		return err
	}
	if connection.Capabilities.PromptCacheKey && request.CacheInput == nil && request.Shape.PromptCacheKey != "" {
		request.CacheInput = &CacheKeyInput{
			Lane:  request.Shape.Lane,
			Model: request.Shape.Model,
			Seed:  request.Shape.PromptCacheKey,
		}
	}
	draft, err := BuildRequestDraft(connection, request)
	if err != nil {
		return fmt.Errorf("building model request draft: %w", err)
	}
	bearerKey, err := s.bearerCredential(connectionConfig)
	if err != nil {
		return err
	}
	return s.transport.Execute(ctx, draft, bearerKey, onEvent)
}

func LaneCacheKey(conversationID string, lane PromptLane) string {
	return BuildLegacyLaneCacheKey(conversationID, lane)
}

func (s *ModelService) ExecutePrompt(lane PromptLane, instructions string, maxOutputTokens uint32, input []PromptItem, promptCacheKey string) ([]StreamEvent, error) {
	connectionConfig, err := config.ReadModelConnection(s.root)
	if err != nil {
		return nil, fmt.Errorf("reading model connection: %w", err)
	}
	cacheKey := ""
	if connectionConfig.Capabilities.PromptCacheKey {
		if promptCacheKey == "" {
			return nil, fmt.Errorf("prompt cache key is required for lane %q", lane)
		}
		cacheKey = promptCacheKey
	}
	request := CompiledPromptRequest{
		Shape: ModelRequestShape{
			Lane:            lane,
			Model:           connectionConfig.Model,
			Instructions:    instructions,
			MaxOutputTokens: maxOutputTokens,
			PromptCacheKey:  cacheKey,
		},
		Input: input,
	}
	if connectionConfig.Capabilities.PromptCacheKey {
		request.CacheInput = &CacheKeyInput{
			Lane:  lane,
			Model: connectionConfig.Model,
			Seed:  promptCacheKey,
		}
	}
	return s.ExecuteRequest(request)
}

func (s *ModelService) bearerCredential(connection config.ModelConnection) (string, error) {
	if connection.AuthMode == "no_auth" {
		return "", nil
	}
	store, err := resolveSecretStore(s.root, s.secrets)
	if err != nil {
		return "", err
	}
	value, ok, err := store.Load(connection.ConnectionID)
	if err != nil {
		return "", fmt.Errorf("loading model bearer credential: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("model bearer credential is not configured for connection %s", connection.ConnectionID)
	}
	return value.Expose(), nil
}

func resolveSecretStore(_ string, secrets *secret.Store) (*secret.Store, error) {
	if secrets != nil {
		return secrets, nil
	}
	return nil, fmt.Errorf("PostgreSQL secret store is required")
}
