package memory

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	embeddingItemKindPersonalMemory = "personal_memory"
	embeddingItemKindKnowledge      = "knowledge"
)

func semanticContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
