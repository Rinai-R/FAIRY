package openai

import (
	"errors"
	"strings"

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
)

const (
	ProtocolResponses       = domainmodel.ProtocolResponses
	ProtocolChatCompletions = domainmodel.ProtocolChatCompletions
	AuthRequirementBearerKey = domainmodel.AuthRequirementBearerKey
	AuthRequirementNone      = domainmodel.AuthRequirementNone
)

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

func ScrubSecret(err error, secret string) error {
	return scrubSecret(err, secret)
}

func scrubSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	message := strings.ReplaceAll(err.Error(), secret, "[REDACTED]")
	return errors.New(message)
}
