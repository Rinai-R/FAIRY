package memory

import (
	"context"
	"fmt"

	"fairy/vectorindex"

	"github.com/jackc/pgx/v5"
)

func enqueuePersonalMemoryEmbeddingJobPostgres(ctx context.Context, tx pgx.Tx, memoryID, content string, now int64) error {
	return enqueueEmbeddingJobPostgres(ctx, tx, vectorindex.ItemKindPersonalMemory, memoryID, content, now)
}

func enqueueKnowledgeEmbeddingJobPostgres(ctx context.Context, tx pgx.Tx, knowledgeID, topic, statement string, now int64) error {
	return enqueueEmbeddingJobPostgres(ctx, tx, vectorindex.ItemKindKnowledge, knowledgeID, topic+"\n"+statement, now)
}

func enqueueEmbeddingJobPostgres(ctx context.Context, tx pgx.Tx, itemKind, itemID, content string, now int64) error {
	pointID, err := vectorindex.PointID(itemKind, itemID, SemanticEmbeddingModelID)
	if err != nil {
		return fmt.Errorf("deriving semantic point id: %w", err)
	}
	contentHash := semanticContentHash(content)
	_, err = tx.Exec(ctx, `
INSERT INTO memory_embedding_items(
  id, item_kind, item_id, model_id, dimensions, point_id, content_hash,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $8)
ON CONFLICT(item_kind, item_id, model_id) DO UPDATE SET
  dimensions = excluded.dimensions,
  point_id = excluded.point_id,
  content_hash = excluded.content_hash,
  status = 'pending',
  error_code = NULL,
  error_message = NULL,
  embedded_at_ms = NULL,
  updated_at_ms = excluded.updated_at_ms
WHERE memory_embedding_items.content_hash IS DISTINCT FROM excluded.content_hash`,
		newID(), itemKind, itemID, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, pointID, contentHash, now)
	if err != nil {
		return fmt.Errorf("upserting semantic embedding item: %w", err)
	}
	_, err = tx.Exec(ctx, `
INSERT INTO memory_embedding_jobs(
  id, item_kind, item_id, model_id, dimensions, point_id, content_hash,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $8)
ON CONFLICT(item_kind, item_id, model_id, content_hash) DO NOTHING`,
		newID(), itemKind, itemID, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, pointID, contentHash, now)
	if err != nil {
		return fmt.Errorf("queueing semantic embedding job: %w", err)
	}
	return nil
}
