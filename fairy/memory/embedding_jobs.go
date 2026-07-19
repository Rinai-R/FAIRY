package memory

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
)

const (
	embeddingItemKindPersonalMemory = "personal_memory"
	embeddingItemKindKnowledge      = "knowledge"
)

type embeddingJobExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func enqueuePersonalMemoryEmbeddingJob(db embeddingJobExecutor, memoryID string, content string, now int64) error {
	return enqueueEmbeddingJob(db, embeddingItemKindPersonalMemory, memoryID, content, now)
}

func enqueueKnowledgeEmbeddingJob(db embeddingJobExecutor, knowledgeID string, topic string, statement string, now int64) error {
	return enqueueEmbeddingJob(db, embeddingItemKindKnowledge, knowledgeID, topic+"\n"+statement, now)
}

func enqueueEmbeddingJob(db embeddingJobExecutor, itemKind string, itemID string, content string, now int64) error {
	contentHash := semanticContentHash(content)
	query := "INSERT OR IGNORE INTO memory_embedding_jobs(id, item_kind, item_id, model_id, dimensions, content_hash, status, created_at_ms, updated_at_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'pending', ?7, ?7)"
	if _, err := db.Exec(query, newID(), itemKind, itemID, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, contentHash, now); err != nil {
		return fmt.Errorf("queueing semantic embedding job: %w", err)
	}
	return nil
}

func semanticContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
