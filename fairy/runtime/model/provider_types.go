package model

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
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
	message := err.Error()
	for _, fragment := range credentialFragments(secret) {
		message = strings.ReplaceAll(message, fragment, "[REDACTED]")
	}
	return errors.New(message)
}

// credentialFragments covers the complete credential plus the bounded prefix
// and suffix fragments commonly echoed by providers in masked authentication
// errors (for example "****1234"). The diagnostic value of four random key
// characters is lower than the risk of retaining a stable credential
// fingerprint in logs or status projections.
func credentialFragments(secret string) []string {
	if secret == "" {
		return nil
	}
	fragments := map[string]struct{}{secret: {}}
	maxFragment := min(len(secret)-1, 12)
	for length := maxFragment; length >= 4; length-- {
		fragments[secret[:length]] = struct{}{}
		fragments[secret[len(secret)-length:]] = struct{}{}
	}
	result := make([]string, 0, len(fragments))
	for fragment := range fragments {
		result = append(result, fragment)
	}
	sort.Slice(result, func(i, j int) bool {
		if len(result[i]) != len(result[j]) {
			return len(result[i]) > len(result[j])
		}
		return result[i] < result[j]
	})
	return result
}

func scrubProviderRequestError(err error, secret, bodyJSON string) error {
	if err == nil {
		return nil
	}
	message := scrubSecret(err, secret).Error()
	for _, value := range modelRequestSensitiveStrings(bodyJSON) {
		message = strings.ReplaceAll(message, value, "[REDACTED INPUT]")
		if encoded, marshalErr := json.Marshal(value); marshalErr == nil && len(encoded) >= 2 {
			message = strings.ReplaceAll(message, string(encoded[1:len(encoded)-1]), "[REDACTED INPUT]")
		}
	}
	return errors.New(message)
}

func modelRequestSensitiveStrings(bodyJSON string) []string {
	var document any
	if json.Unmarshal([]byte(bodyJSON), &document) != nil {
		return nil
	}
	values := make(map[string]struct{})
	var walk func(any, bool)
	walk = func(value any, sensitive bool) {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				walk(child, sensitive || modelRequestSensitiveKey(key))
			}
		case []any:
			for _, child := range item {
				walk(child, sensitive)
			}
		case string:
			if sensitive && item != "" {
				values[item] = struct{}{}
			}
		}
	}
	walk(document, false)
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if len(result[i]) != len(result[j]) {
			return len(result[i]) > len(result[j])
		}
		return result[i] < result[j]
	})
	return result
}

func modelRequestSensitiveKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "instructions", "input", "messages", "content", "text", "arguments", "image_url", "input_image", "input_text", "output", "tool_result":
		return true
	default:
		return false
	}
}
