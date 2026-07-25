package postgres

import (
	"context"
	"fmt"

	vectorindex "fairy/internal/adapters/memory/qdrant"

	"github.com/jackc/pgx/v5"
)

func enqueuePersonalMemoryEmbeddingJobPostgres(ctx context.Context, tx pgx.Tx, memoryID, content string, now int64) error {
	return enqueueEmbeddingJobPostgres(ctx, tx, embeddingItemKindPersonalMemory, memoryID, content, now)
}

func enqueueKnowledgeEmbeddingJobPostgres(ctx context.Context, tx pgx.Tx, knowledgeID, topic, statement string, now int64) error {
	return enqueueEmbeddingJobPostgres(ctx, tx, embeddingItemKindKnowledge, knowledgeID, topic+"\n"+statement, now)
}

func enqueueEmbeddingJobPostgres(ctx context.Context, tx pgx.Tx, itemKind, itemID, content string, now int64) error {
	pointID, err := vectorindex.PointID(itemKind, itemID, SemanticEmbeddingModelID)
	if err != nil {
		return fmt.Errorf("deriving semantic point id: %w", err)
	}
	contentHash := semanticContentHash(content)
	return EnqueueEmbeddingJob(ctx, tx, newID(), itemKind, itemID, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, pointID.String(), contentHash, now)
}
