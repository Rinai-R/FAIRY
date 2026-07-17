package model

import (
	"context"
	"errors"
	"strings"
)

type Usage struct {
	PromptTokens      int     `json:"promptTokens"`
	CompletionTokens  int     `json:"completionTokens"`
	CachedInputTokens *uint64 `json:"cachedInputTokens,omitempty"`
	CacheWriteTokens  *uint64 `json:"cacheWriteTokens,omitempty"`
}

type StreamEvent struct {
	Type          string         `json:"type"`
	Data          string         `json:"data,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	FunctionCalls []FunctionCall `json:"functionCalls,omitempty"`
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens     *uint64 `json:"cached_tokens"`
		CacheWriteTokens *uint64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	PromptTokensDetails struct {
		CachedTokens     *uint64 `json:"cached_tokens"`
		CacheWriteTokens *uint64 `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CachedTokens          *uint64 `json:"cached_tokens"`
	CacheReadInputTokens  *uint64 `json:"cache_read_input_tokens"`
	CacheWriteTokens      *uint64 `json:"cache_write_tokens"`
	CacheWriteInputTokens *uint64 `json:"cache_write_input_tokens"`
}

func (u responsesUsage) public() *Usage {
	cachedInputTokens := firstUint64Ptr(
		u.InputTokensDetails.CachedTokens,
		u.PromptTokensDetails.CachedTokens,
		u.CacheReadInputTokens,
		u.CachedTokens,
	)
	cacheWriteTokens := firstUint64Ptr(
		u.InputTokensDetails.CacheWriteTokens,
		u.PromptTokensDetails.CacheWriteTokens,
		u.CacheWriteInputTokens,
		u.CacheWriteTokens,
	)
	if u.InputTokens == 0 && u.OutputTokens == 0 && cachedInputTokens == nil && cacheWriteTokens == nil {
		return nil
	}
	return &Usage{
		PromptTokens:      u.InputTokens,
		CompletionTokens:  u.OutputTokens,
		CachedInputTokens: cachedInputTokens,
		CacheWriteTokens:  cacheWriteTokens,
	}
}

func firstUint64Ptr(values ...*uint64) *uint64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// ExecuteRequest runs a prepared draft via the default SDK transport.
func ExecuteRequest(ctx context.Context, draft RequestDraft, bearerKey string, onEvent func(StreamEvent)) error {
	return SDKTransport{}.Execute(ctx, draft, bearerKey, onEvent)
}

func scrubSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	message := strings.ReplaceAll(err.Error(), secret, "[REDACTED]")
	return errors.New(message)
}
