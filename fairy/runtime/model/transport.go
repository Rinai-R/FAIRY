package model

import (
	"context"
	"errors"
)

const (
	MaxModelStreamEvents            = 4096
	MaxModelStreamPayloadBytes      = 1 << 20
	MaxModelFunctionCalls           = 64
	MaxModelFunctionArgumentsBytes  = 256 << 10
	MaxModelCompletedResponseBytes  = 4 << 20
	maxModelFunctionIdentifierBytes = 4 << 10
)

var ErrModelStreamCapacity = errors.New("model stream capacity reached")

// Protocol identifies which OpenAI-compatible HTTP resource executes a draft.
type Protocol string

const (
	ProtocolResponses       Protocol = "responses"
	ProtocolChatCompletions Protocol = "chat_completions"
)

// AuthRequirement describes whether a bearer credential must be attached.
type AuthRequirement string

const (
	AuthRequirementBearerKey AuthRequirement = "bearer_key_required"
	AuthRequirementNone      AuthRequirement = "none"
)

// RequestDraft is a prepared HTTP request ready for provider transport.
type RequestDraft struct {
	Protocol        Protocol        `json:"protocol"`
	Method          string          `json:"method"`
	URL             string          `json:"url"`
	ContentType     string          `json:"contentType"`
	AuthRequirement AuthRequirement `json:"authRequirement"`
	BodyJSON        string          `json:"bodyJSON"`
}

// Usage captures token accounting from a provider response.
type Usage struct {
	PromptTokens      int     `json:"promptTokens"`
	CompletionTokens  int     `json:"completionTokens"`
	CachedInputTokens *uint64 `json:"cachedInputTokens,omitempty"`
	CacheWriteTokens  *uint64 `json:"cacheWriteTokens,omitempty"`
}

// StreamEvent is a normalized provider streaming event.
type StreamEvent struct {
	Type          string         `json:"type"`
	Data          string         `json:"data,omitempty"`
	FinishReason  string         `json:"finishReason,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	FunctionCalls []FunctionCall `json:"functionCalls,omitempty"`
}

// FunctionCall is a model-requested tool invocation.
type FunctionCall struct {
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Transport executes a prepared RequestDraft against an OpenAI-compatible endpoint.
type Transport interface {
	Execute(ctx context.Context, draft RequestDraft, bearerKey string, onEvent func(StreamEvent)) error
}
