package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

const maxVectorMaintenancePageSize = 100

type VectorRebuildResult struct {
	ScannedItems int `json:"scannedItems"`
	UpdatedItems int `json:"updatedItems"`
	SkippedItems int `json:"skippedItems"`
	FailedItems  int `json:"failedItems"`
}

type authoritativeVectorItem struct {
	ItemKind string
	ItemID   string
	Content  string
	Current  bool
}

func (s *Store) RebuildVectors(ctx context.Context, pageSize int) (VectorRebuildResult, error) {
	if s == nil || s.pool == nil {
		return VectorRebuildResult{}, errors.New("vector rebuild requires PostgreSQL store")
	}
	if s.semanticEmbedder == nil || !s.semanticEmbedder.Ready() {
		return VectorRebuildResult{}, errors.New("vector rebuild requires ready embedder")
	}
	if dims := s.semanticEmbedder.Dims(); dims != SemanticEmbeddingDimensions {
		return VectorRebuildResult{}, fmt.Errorf("embedding dimensions = %d, want %d", dims, SemanticEmbeddingDimensions)
	}
	modelID, err := semanticEmbedderModelID(s.semanticEmbedder)
	if err != nil {
		return VectorRebuildResult{}, err
	}
	if pageSize < 1 || pageSize > maxVectorMaintenancePageSize {
		return VectorRebuildResult{}, fmt.Errorf("vector rebuild page size must be between 1 and %d", maxVectorMaintenancePageSize)
	}
	result := VectorRebuildResult{}
	lastKind, lastID := "", ""
	for {
		items, err := s.authoritativeVectorPage(ctx, modelID, lastKind, lastID, pageSize)
		if err != nil {
			return result, err
		}
		if len(items) == 0 {
			return result, nil
		}
		result.ScannedItems += len(items)
		pending := make([]authoritativeVectorItem, 0, len(items))
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
			vectors, err := s.semanticEmbedder.Embed(contents)
			if err != nil {
				result.FailedItems += len(pending)
				return result, fmt.Errorf("embedding vector rebuild page: %w", err)
			}
			if len(vectors) != len(pending) {
				result.FailedItems += len(pending)
				return result, fmt.Errorf("embedding result count = %d, want %d", len(vectors), len(pending))
			}
			for index, item := range pending {
				if err := ValidateVector(vectors[index]); err != nil {
					result.FailedItems++
					return result, err
				}
				updated, err := s.updateRebuiltVector(ctx, modelID, item, vectors[index])
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
		lastKind, lastID = items[len(items)-1].ItemKind, items[len(items)-1].ItemID
	}
}

func (s *Store) authoritativeVectorPage(ctx context.Context, currentModelID, lastKind, lastID string, limit int) ([]authoritativeVectorItem, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT item_kind, item_id, content,
       embedding_model_id_v2, embedding_content_hash_v2, embedding_v2 IS NOT NULL
FROM (
  SELECT 'personal_memory'::text AS item_kind,
         p.id AS item_id,
         p.content AS content,
         p.embedding_model_id_v2,
         p.embedding_content_hash_v2,
         p.embedding_v2
  FROM personal_memories p
  WHERE p.status = 'active' AND p.review_status = 'ready'
  UNION ALL
  SELECT 'knowledge'::text AS item_kind,
         k.id AS item_id,
         k.topic || chr(10) || k.statement AS content,
         k.embedding_model_id_v2,
         k.embedding_content_hash_v2,
         k.embedding_v2
  FROM knowledge_entries k
  WHERE k.status = 'verified'
) items
WHERE (item_kind, item_id) > ($1, $2)
ORDER BY item_kind ASC, item_id ASC
LIMIT $3`, lastKind, lastID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying authoritative vector items: %w", err)
	}
	defer rows.Close()
	items := make([]authoritativeVectorItem, 0, limit)
	for rows.Next() {
		var item authoritativeVectorItem
		var persistedModelID, contentHash pgtype.Text
		var vectorPresent bool
		if err := rows.Scan(&item.ItemKind, &item.ItemID, &item.Content, &persistedModelID, &contentHash, &vectorPresent); err != nil {
			return nil, fmt.Errorf("scanning authoritative vector item: %w", err)
		}
		item.Current = persistedModelID.Valid &&
			persistedModelID.String == currentModelID &&
			contentHash.Valid &&
			contentHash.String == semanticContentHash(item.Content) &&
			vectorPresent
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating authoritative vector items: %w", err)
	}
	return items, nil
}

func (s *Store) updateRebuiltVector(ctx context.Context, modelID string, item authoritativeVectorItem, vector []float32) (bool, error) {
	value, err := embeddingForContent(&precomputedSemanticEmbedder{modelID: modelID, vector: vector}, item.Content)
	if err != nil {
		return false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var query string
	switch item.ItemKind {
	case "personal_memory":
		query = `
UPDATE personal_memories
SET embedding_model_id_v2 = $3,
    embedding_content_hash_v2 = $4,
    embedding_v2 = $5::public.vector,
    updated_at_ms = updated_at_ms
WHERE id = $1
  AND content = $2
  AND status = 'active'
  AND review_status = 'ready'`
	case "knowledge":
		query = `
UPDATE knowledge_entries
SET embedding_model_id_v2 = $3,
    embedding_content_hash_v2 = $4,
    embedding_v2 = $5::public.vector,
    updated_at_ms = updated_at_ms
WHERE id = $1
  AND topic || chr(10) || statement = $2
  AND status = 'verified'`
	default:
		return false, fmt.Errorf("unsupported vector item kind %q", item.ItemKind)
	}
	changed, err := s.pool.Raw().Exec(queryCtx, query, item.ItemID, item.Content, value.ModelID, value.ContentHash, value.Vector.String())
	if err != nil {
		return false, fmt.Errorf("updating rebuilt %s vector: %w", item.ItemKind, err)
	}
	return changed.RowsAffected() == 1, nil
}

type precomputedSemanticEmbedder struct {
	modelID string
	vector  []float32
}

func (*precomputedSemanticEmbedder) Ready() bool              { return true }
func (*precomputedSemanticEmbedder) Status() SemanticStatus   { return SemanticStatusReady }
func (embedder *precomputedSemanticEmbedder) ModelID() string { return embedder.modelID }
func (*precomputedSemanticEmbedder) Dims() int                { return SemanticEmbeddingDimensions }
func (embedder *precomputedSemanticEmbedder) Embed([]string) ([][]float32, error) {
	return [][]float32{embedder.vector}, nil
}
