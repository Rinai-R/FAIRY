package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Store) commitMemoryMutationsPostgres(ctx context.Context, batchID, characterID string, allowedMemoryIDs []string, mutations []MemoryMutation) ([]MemoryMutationResult, error) {
	if err := validateID("batch_id", batchID); err != nil {
		return nil, err
	}
	if err := validateID("character_id", characterID); err != nil {
		return nil, err
	}
	if len(mutations) > MaxMemoryMutationsPerBatch {
		return nil, errors.New("extraction batch exceeds memory mutation limit")
	}
	for index := range mutations {
		if err := validateMemoryMutation(&mutations[index], characterID); err != nil {
			return nil, err
		}
	}
	allowed := make(map[string]struct{}, len(allowedMemoryIDs))
	for _, id := range allowedMemoryIDs {
		allowed[id] = struct{}{}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning memory mutation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	var conversationID, batchCharacterID, sourceTurnID string
	err = tx.QueryRow(queryCtx, `
SELECT b.conversation_id, b.character_id, bt.turn_id
FROM extraction_batches b
JOIN extraction_batch_turns bt ON bt.batch_id = b.id
WHERE b.id = $1 AND b.status = 'running'
ORDER BY bt.turn_sequence DESC LIMIT 1
FOR UPDATE OF b`, batchID).Scan(&conversationID, &batchCharacterID, &sourceTurnID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("extraction batch does not exist or is not running")
	}
	if err != nil {
		return nil, fmt.Errorf("reading running extraction batch: %w", err)
	}
	if batchCharacterID != characterID {
		return nil, errors.New("extraction batch does not belong to character")
	}
	now := nowUnixMS()
	results := make([]MemoryMutationResult, 0, len(mutations))
	for _, mutation := range mutations {
		switch mutation.Operation {
		case "create":
			existingID, err := findDuplicateMemoryPostgres(queryCtx, tx, mutation.Kind, mutation.Scope, mutation.Content)
			if err != nil {
				return nil, err
			}
			if existingID != "" {
				results = append(results, MemoryMutationResult{Status: "no_change", ExistingMemoryID: existingID})
				continue
			}
			record, err := insertPersonalMemoryPostgres(queryCtx, tx, newID(), mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints, conversationID, sourceTurnID, nil, now)
			if err != nil {
				return nil, err
			}
			results = append(results, MemoryMutationResult{Status: "applied", MemoryID: record.ID})
		case "supersede":
			if _, ok := allowed[mutation.MemoryID]; !ok {
				return nil, errors.New("supersede references a memory id not provided to the batch")
			}
			if err := requireActiveMemoryScopePostgres(queryCtx, tx, mutation.MemoryID, mutation.Kind, mutation.Scope); err != nil {
				return nil, err
			}
			existingID, err := findDuplicateMemoryPostgres(queryCtx, tx, mutation.Kind, mutation.Scope, mutation.Content)
			if err != nil {
				return nil, err
			}
			if existingID != "" && existingID != mutation.MemoryID {
				results = append(results, MemoryMutationResult{Status: "no_change", ExistingMemoryID: existingID})
				continue
			}
			changed, err := tx.Exec(queryCtx, "UPDATE personal_memories SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", mutation.MemoryID, now)
			if err != nil {
				return nil, fmt.Errorf("superseding personal memory: %w", err)
			}
			if changed.RowsAffected() != 1 {
				return nil, errors.New("supersede target memory is not active")
			}
			supersedesID := mutation.MemoryID
			record, err := insertPersonalMemoryPostgres(queryCtx, tx, newID(), mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints, conversationID, sourceTurnID, &supersedesID, now)
			if err != nil {
				return nil, err
			}
			results = append(results, MemoryMutationResult{Status: "applied", MemoryID: record.ID})
		default:
			return nil, fmt.Errorf("unsupported memory mutation operation %q", mutation.Operation)
		}
	}
	if err := succeedExtractionBatchPostgres(queryCtx, tx, batchID, s.workerID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing memory mutations: %w", err)
	}
	return results, nil
}

func findDuplicateMemoryPostgres(ctx context.Context, tx pgx.Tx, kind string, scope MemoryScope, content string) (string, error) {
	scopeKind, characterID, _ := memoryScopeColumns(scope)
	var rows pgx.Rows
	var err error
	if characterID == nil {
		rows, err = tx.Query(ctx, `
SELECT id, content FROM personal_memories
WHERE kind = $1 AND scope_kind = $2 AND character_id IS NULL
  AND status = 'active' AND review_status = 'ready'
ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind)
	} else {
		rows, err = tx.Query(ctx, `
SELECT id, content FROM personal_memories
WHERE kind = $1 AND scope_kind = $2 AND character_id = $3
  AND status = 'active' AND review_status = 'ready'
ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind, *characterID)
	}
	if err != nil {
		return "", fmt.Errorf("querying duplicate memories: %w", err)
	}
	defer rows.Close()
	normalized := normalizeMemoryContent(content)
	for rows.Next() {
		var id, existing string
		if err := rows.Scan(&id, &existing); err != nil {
			return "", fmt.Errorf("scanning duplicate memory: %w", err)
		}
		if normalizeMemoryContent(existing) == normalized {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterating duplicate memories: %w", err)
	}
	return "", nil
}

func requireActiveMemoryScopePostgres(ctx context.Context, tx pgx.Tx, memoryID, kind string, scope MemoryScope) error {
	current, err := personalMemoryByIDPostgres(ctx, tx, memoryID, true)
	if err != nil {
		return err
	}
	if current.Status != "active" || current.Kind != kind || current.Scope.Type != scope.Type || current.Scope.CharacterID != scope.CharacterID {
		return errors.New("supersede target memory status, kind, or scope does not match")
	}
	return nil
}

func succeedExtractionBatchPostgres(ctx context.Context, tx pgx.Tx, batchID, workerID string, now int64) error {
	_, err := tx.Exec(ctx, `
UPDATE conversation_turns
SET extraction_state = 'processed', updated_at_ms = $2
WHERE id IN (SELECT turn_id FROM extraction_batch_turns WHERE batch_id = $1)
  AND extraction_state = 'claimed'`, batchID, now)
	if err != nil {
		return fmt.Errorf("marking extraction turns processed: %w", err)
	}
	changed, err := tx.Exec(ctx, `
UPDATE extraction_batches
SET status = 'succeeded', lease_owner = NULL, lease_expires_at_ms = NULL, updated_at_ms = $2
WHERE id = $1 AND status = 'running' AND lease_owner = $3`, batchID, now, workerID)
	if err != nil {
		return fmt.Errorf("succeeding extraction batch: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("extraction batch is not owned by this worker")
	}
	return nil
}
