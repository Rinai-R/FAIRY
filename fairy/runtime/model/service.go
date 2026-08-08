package model

import (
	"context"
	"fmt"

	"fairy/runtime/config"
)

type ModelService struct {
	root      string
	transport Transport
	secrets   *config.SecretStore
}

func NewModelService(root string, secrets *config.SecretStore) *ModelService {
	return &ModelService{root: root, transport: SDKTransport{}, secrets: secrets}
}

func NewModelServiceWithTransport(root string, transport Transport, secrets *config.SecretStore) *ModelService {
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
	request = normalizeCacheInput(request)
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
	request = normalizeCacheInput(request)
	draft, err := BuildRequestDraft(connection, request)
	if err != nil {
		return fmt.Errorf("building model request draft: %w", err)
	}
	bearerKey, err := s.bearerCredential(connectionConfig)
	if err != nil {
		return err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	eventCount := 0
	payloadBytes := 0
	var capacityErr error
	transportErr := s.transport.Execute(streamCtx, draft, bearerKey, func(event StreamEvent) {
		if capacityErr != nil {
			return
		}
		if err := validateModelStreamEventCapacity(event); err != nil {
			capacityErr = err
			cancel()
			return
		}
		if eventCount >= MaxModelStreamEvents {
			capacityErr = fmt.Errorf("%w: event limit %d", ErrModelStreamCapacity, MaxModelStreamEvents)
			cancel()
			return
		}
		eventBytes := modelStreamEventPayloadBytes(event)
		if eventBytes > MaxModelStreamPayloadBytes-payloadBytes {
			capacityErr = fmt.Errorf("%w: payload limit %d bytes", ErrModelStreamCapacity, MaxModelStreamPayloadBytes)
			cancel()
			return
		}
		eventCount++
		payloadBytes += eventBytes
		onEvent(event)
	})
	if capacityErr != nil {
		return capacityErr
	}
	return transportErr
}

func validateModelStreamEventCapacity(event StreamEvent) error {
	if len(event.FunctionCalls) > MaxModelFunctionCalls {
		return fmt.Errorf("%w: function call limit %d", ErrModelStreamCapacity, MaxModelFunctionCalls)
	}
	argumentBytes := 0
	for _, call := range event.FunctionCalls {
		if len(call.CallID) > maxModelFunctionIdentifierBytes || len(call.Name) > maxModelFunctionIdentifierBytes {
			return fmt.Errorf("%w: function call identifier limit %d bytes", ErrModelStreamCapacity, maxModelFunctionIdentifierBytes)
		}
		if len(call.Arguments) > MaxModelFunctionArgumentsBytes {
			return fmt.Errorf("%w: function arguments limit %d bytes", ErrModelStreamCapacity, MaxModelFunctionArgumentsBytes)
		}
		if len(call.Arguments) > MaxModelStreamPayloadBytes-argumentBytes {
			return fmt.Errorf("%w: function arguments total limit %d bytes", ErrModelStreamCapacity, MaxModelStreamPayloadBytes)
		}
		argumentBytes += len(call.Arguments)
	}
	return nil
}

func modelStreamEventPayloadBytes(event StreamEvent) int {
	total := 0
	add := func(value string) {
		if total > MaxModelStreamPayloadBytes {
			return
		}
		if len(value) > MaxModelStreamPayloadBytes-total {
			total = MaxModelStreamPayloadBytes + 1
			return
		}
		total += len(value)
	}
	add(event.Type)
	add(event.Data)
	add(event.FinishReason)
	for _, call := range event.FunctionCalls {
		add(call.CallID)
		add(call.Name)
		add(call.Arguments)
	}
	return total
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
		cacheInput := NewCacheKeyInput(lane, connectionConfig.Model, "", instructions)
		cacheInput.Seed = promptCacheKey
		request.CacheInput = &cacheInput
	}
	return s.ExecuteRequest(request)
}

func normalizeCacheInput(request CompiledPromptRequest) CompiledPromptRequest {
	if request.CacheInput != nil {
		input := *request.CacheInput
		if input.StablePromptHash == "" {
			input.StablePromptHash = NewCacheKeyInput(input.Lane, input.Model, input.ConversationID, request.Shape.Instructions).StablePromptHash
		}
		request.CacheInput = &input
		return request
	}
	if request.Shape.PromptCacheKey == "" {
		return request
	}
	input := NewCacheKeyInput(request.Shape.Lane, request.Shape.Model, "", request.Shape.Instructions)
	input.Seed = request.Shape.PromptCacheKey
	request.CacheInput = &input
	return request
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

func resolveSecretStore(_ string, secrets *config.SecretStore) (*config.SecretStore, error) {
	if secrets != nil {
		return secrets, nil
	}
	return nil, fmt.Errorf("PostgreSQL secret store is required")
}
