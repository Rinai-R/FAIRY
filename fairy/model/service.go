package model

import (
	"context"
	"fmt"

	"fairy/config"
	"fairy/secret"
)

type ModelService struct {
	root      string
	transport HTTPTransport
}

func NewModelService(root string) *ModelService {
	return &ModelService{root: root}
}

func NewModelServiceWithTransport(root string, transport HTTPTransport) *ModelService {
	return &ModelService{root: root, transport: transport}
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
	if ctx == nil {
		return nil, fmt.Errorf("model request context is required")
	}
	connectionConfig, err := config.ReadModelConnection(s.root)
	if err != nil {
		return nil, fmt.Errorf("reading model connection: %w", err)
	}
	connection, err := ConnectionFromConfig(connectionConfig)
	if err != nil {
		return nil, err
	}
	draft, err := BuildRequestDraft(connection, request)
	if err != nil {
		return nil, fmt.Errorf("building model request draft: %w", err)
	}
	bearerKey, err := s.bearerCredential(connectionConfig)
	if err != nil {
		return nil, err
	}
	events := make([]StreamEvent, 0)
	if err := s.transport.Execute(ctx, draft, bearerKey, func(event StreamEvent) {
		events = append(events, event)
	}); err != nil {
		return nil, err
	}
	return events, nil
}

func LaneCacheKey(conversationID string, lane PromptLane) string {
	return "fairy:" + conversationID + ":" + string(lane)
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
	return s.ExecuteRequest(request)
}

func (s *ModelService) bearerCredential(connection config.ModelConnection) (string, error) {
	if connection.AuthMode == "no_auth" {
		return "", nil
	}
	dbPath, err := secret.DatabasePath(s.root)
	if err != nil {
		return "", err
	}
	value, ok, err := secret.NewStore(dbPath).Load(connection.ConnectionID)
	if err != nil {
		return "", fmt.Errorf("loading model bearer credential: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("model bearer credential is not configured for connection %s", connection.ConnectionID)
	}
	return value.Expose(), nil
}
