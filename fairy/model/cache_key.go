package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

const PromptCacheKeyVersion = "v2"

// CacheKeyInput identifies only stable prompt-prefix facts. Dynamic transcript,
// retrieval hits, feedback text and tool results must never be placed here.
type CacheKeyInput struct {
	Lane              PromptLane
	Model             string
	ConversationID    string
	CharacterRevision uint64
	ProfileRevision   uint64
	PromptRevision    uint64
	Seed              string
}

// BuildPromptCacheKey returns a deterministic, opaque key suitable for a
// provider prompt cache. Length-prefixing avoids ambiguous concatenations.
func BuildPromptCacheKey(input CacheKeyInput) (string, error) {
	if err := validateCacheKeyInput(input); err != nil {
		return "", err
	}
	parts := []string{
		PromptCacheKeyVersion,
		string(input.Lane),
		input.Model,
		input.ConversationID,
		strconv.FormatUint(input.CharacterRevision, 10),
		strconv.FormatUint(input.ProfileRevision, 10),
		strconv.FormatUint(input.PromptRevision, 10),
		input.Seed,
	}
	var material strings.Builder
	for _, part := range parts {
		material.WriteString(strconv.Itoa(len(part)))
		material.WriteByte(':')
		material.WriteString(part)
		material.WriteByte('|')
	}
	sum := sha256.Sum256([]byte(material.String()))
	return "fairy:" + PromptCacheKeyVersion + ":" + hex.EncodeToString(sum[:16]), nil
}

func validateCacheKeyInput(input CacheKeyInput) error {
	if err := validateLane(input.Lane); err != nil {
		return err
	}
	if strings.TrimSpace(input.Model) == "" {
		return errors.New("cache key model is required")
	}
	if strings.TrimSpace(input.ConversationID) == "" && strings.TrimSpace(input.Seed) == "" {
		return errors.New("cache key conversation or seed is required")
	}
	return nil
}

// BuildLegacyLaneCacheKey preserves the pre-v2 public key shape for injected
// model fakes and older callers that have no revision metadata.
func BuildLegacyLaneCacheKey(conversationID string, lane PromptLane) string {
	return "fairy:" + conversationID + ":" + string(lane)
}
