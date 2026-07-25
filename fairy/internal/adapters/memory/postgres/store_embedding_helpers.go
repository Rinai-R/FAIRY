package postgres

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	embeddingItemKindPersonalMemory = "personal_memory"
	embeddingItemKindKnowledge      = "knowledge"

	embeddingJobErrorStaleItem     = "stale_item"
	embeddingJobErrorStaleContent  = "stale_content"
	embeddingJobErrorEmbedFailed   = "embed_failed"
	embeddingJobErrorInvalidVector = "invalid_vector"
)

type embeddingJob struct {
	ID          string
	ItemKind    string
	ItemID      string
	ModelID     string
	Dimensions  int
	PointID     string
	ContentHash string
}

type embeddingJobPayload struct {
	Content     string
	ScopeType   string
	CharacterID string
}

func semanticContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
