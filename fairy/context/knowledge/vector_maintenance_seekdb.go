package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"fairy/runtime/embedding"
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
	if !s.usesSeekDB() {
		return VectorRebuildResult{}, ErrStoreBackendUnavailable
	}
	return s.rebuildSeekDBVectors(ctx, pageSize)
}

func (s *Store) rebuildSeekDBVectors(ctx context.Context, pageSize int) (VectorRebuildResult, error) {
	embedder := s.semanticEmbedderSnapshot()
	if embedder == nil || !embedder.Ready() {
		return VectorRebuildResult{}, errors.New("knowledge vector rebuild requires ready embedder")
	}
	if pageSize < 1 || pageSize > maxVectorMaintenancePageSize {
		return VectorRebuildResult{}, fmt.Errorf(
			"vector rebuild page size must be between 1 and %d", maxVectorMaintenancePageSize,
		)
	}
	modelID, err := embedding.ModelID(embedder)
	if err != nil {
		return VectorRebuildResult{}, err
	}
	result := VectorRebuildResult{}
	lastID := ""
	for {
		items, err := s.seekDBVectorPage(ctx, modelID, lastID, pageSize)
		if err != nil {
			return result, err
		}
		if len(items) == 0 {
			return result, nil
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
			values, err := embedding.ForContentsContext(ctx, embedder, contents)
			if err != nil {
				result.FailedItems += len(pending)
				return result, fmt.Errorf("embedding SeekDB knowledge vector page: %w", err)
			}
			for index, item := range pending {
				if err := ctx.Err(); err != nil {
					result.FailedItems += len(pending) - index
					return result, err
				}
				updated, err := s.updateSeekDBVector(
					context.WithoutCancel(ctx), item, values[index],
				)
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

func (s *Store) seekDBVectorPage(
	ctx context.Context,
	modelID, lastID string,
	limit int,
) ([]vectorItem, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT id, topic, statement, embedding_space_id,
       embedding_content_hash, embedding IS NOT NULL
FROM knowledge_entries
WHERE status = 'verified' AND id > ?
ORDER BY id ASC
LIMIT ?`, lastID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB knowledge vector items: %w", err)
	}
	defer rows.Close()
	items := make([]vectorItem, 0, limit)
	for rows.Next() {
		var (
			item             vectorItem
			persistedModelID sql.NullString
			contentHash      []byte
			vectorPresent    bool
		)
		if err := rows.Scan(
			&item.ID, &item.Topic, &item.Statement, &persistedModelID,
			&contentHash, &vectorPresent,
		); err != nil {
			return nil, fmt.Errorf("scanning SeekDB knowledge vector item: %w", err)
		}
		finalizeSeekDBVectorItem(&item, modelID, persistedModelID, contentHash, vectorPresent)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB knowledge vector items: %w", err)
	}
	return items, nil
}

func finalizeSeekDBVectorItem(
	item *vectorItem,
	modelID string,
	persistedModelID sql.NullString,
	contentHash []byte,
	vectorPresent bool,
) {
	item.Content = item.Topic + "\n" + item.Statement
	expectedHash := sha256.Sum256([]byte(item.Content))
	item.Current = persistedModelID.Valid && persistedModelID.String == modelID &&
		bytes.Equal(contentHash, expectedHash[:]) && vectorPresent
}

func (s *Store) updateSeekDBVector(
	ctx context.Context,
	item vectorItem,
	value embedding.EmbeddingValue,
) (bool, error) {
	spaceID, contentHash, vector, err := seekDBKnowledgeEmbeddingTuple(item.Content, value)
	if err != nil {
		return false, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	result, err := s.seekDB.ExecContext(queryCtx, `
UPDATE knowledge_entries
SET embedding_space_id = ?, embedding_content_hash = ?, embedding = ?,
    updated_at_ms = updated_at_ms
WHERE id = ? AND status = 'verified' AND topic = ? AND statement = ?`,
		spaceID, contentHash, vector, item.ID, item.Topic, item.Statement,
	)
	if err != nil {
		return false, fmt.Errorf("updating rebuilt SeekDB knowledge vector: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rebuilt SeekDB knowledge vector count: %w", err)
	}
	return rows == 1, nil
}
