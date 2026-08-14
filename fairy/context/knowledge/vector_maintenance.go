package knowledge

import (
	"context"
	"errors"
	"fmt"

	"fairy/runtime/embedding"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"
)

const maxVectorMaintenancePageSize = 100

type VectorRebuildResult struct {
	ScannedItems int `json:"scannedItems"`
	UpdatedItems int `json:"updatedItems"`
	SkippedItems int `json:"skippedItems"`
	FailedItems  int `json:"failedItems"`
}

type vectorItem struct {
	ID        string
	Topic     string
	Statement string
	Content   string
	Current   bool
}

func (s *Store) RebuildVectors(ctx context.Context, pageSize int) (VectorRebuildResult, error) {
	if s.usesSeekDB() {
		return s.rebuildSeekDBVectors(ctx, pageSize)
	}
	if !s.usesPostgres() {
		return VectorRebuildResult{}, ErrStoreBackendUnavailable
	}
	embedder := s.embedder.Snapshot()
	if embedder == nil || !embedder.Ready() {
		return VectorRebuildResult{}, errors.New("knowledge vector rebuild requires ready embedder")
	}
	if pageSize < 1 || pageSize > maxVectorMaintenancePageSize {
		return VectorRebuildResult{}, fmt.Errorf("vector rebuild page size must be between 1 and %d", maxVectorMaintenancePageSize)
	}
	modelID, err := embedding.ModelID(embedder)
	if err != nil {
		return VectorRebuildResult{}, err
	}
	result := VectorRebuildResult{}
	lastID := ""
	for {
		items, err := s.vectorPage(ctx, modelID, lastID, pageSize)
		if err != nil || len(items) == 0 {
			return result, err
		}
		result.ScannedItems += len(items)
		pending := make([]vectorItem, 0, len(items))
		contents := make([]string, 0, len(items))
		for _, item := range items {
			if item.Current {
				result.SkippedItems++
				continue
			}
			pending = append(pending, item)
			contents = append(contents, item.Content)
		}
		if len(pending) > 0 {
			vectors, err := embedder.Embed(contents)
			if err != nil || len(vectors) != len(pending) {
				result.FailedItems += len(pending)
				if err != nil {
					return result, fmt.Errorf("embedding knowledge vector page: %w", err)
				}
				return result, fmt.Errorf("embedding result count = %d, want %d", len(vectors), len(pending))
			}
			for index, item := range pending {
				if err := embedding.ValidateVector(vectors[index]); err != nil {
					result.FailedItems++
					return result, err
				}
				updated, err := s.updateVector(ctx, modelID, item, vectors[index])
				if err != nil {
					result.FailedItems++
					return result, err
				}
				if updated {
					result.UpdatedItems++
				} else {
					result.SkippedItems++
				}
			}
		}
		lastID = items[len(items)-1].ID
	}
}

func (s *Store) vectorPage(ctx context.Context, modelID, lastID string, limit int) ([]vectorItem, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT id, topic || chr(10) || statement,
       embedding_model_id_v2, embedding_content_hash_v2, embedding_v2 IS NOT NULL
FROM knowledge_entries
WHERE status = 'verified' AND id > $1
ORDER BY id ASC
LIMIT $2`, lastID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge vector items: %w", err)
	}
	defer rows.Close()
	items := make([]vectorItem, 0, limit)
	for rows.Next() {
		var item vectorItem
		var persistedModelID, contentHash pgtype.Text
		var vectorPresent bool
		if err := rows.Scan(&item.ID, &item.Content, &persistedModelID, &contentHash, &vectorPresent); err != nil {
			return nil, fmt.Errorf("scanning knowledge vector item: %w", err)
		}
		item.Current = persistedModelID.Valid && persistedModelID.String == modelID &&
			contentHash.Valid && contentHash.String == embedding.ContentHash(item.Content) && vectorPresent
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) updateVector(ctx context.Context, modelID string, item vectorItem, vector []float32) (bool, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE knowledge_entries
SET embedding_model_id_v2 = $3,
    embedding_content_hash_v2 = $4,
    embedding_v2 = $5::public.vector,
    updated_at_ms = updated_at_ms
WHERE id = $1
  AND topic || chr(10) || statement = $2
  AND status = 'verified'`, item.ID, item.Content, modelID, embedding.ContentHash(item.Content), pgvector.NewVector(vector).String())
	if err != nil {
		return false, fmt.Errorf("updating rebuilt knowledge vector: %w", err)
	}
	return changed.RowsAffected() == 1, nil
}
