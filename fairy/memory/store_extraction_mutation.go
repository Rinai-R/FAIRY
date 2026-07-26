package memory

import (
	"context"
	"errors"
	"fmt"
)

func (s *Store) commitMemoryMutationsPostgres(ctx context.Context, batchID, characterID string, allowedMemoryIDs []string, mutations []MemoryMutation) ([]MemoryMutationResult, error) {
	if err := ValidateID("batch_id", batchID); err != nil {
		return nil, err
	}
	if err := ValidateID("character_id", characterID); err != nil {
		return nil, err
	}
	if len(mutations) > MaxMemoryMutationsPerBatch {
		return nil, errors.New("extraction batch exceeds memory mutation limit")
	}
	for index := range mutations {
		if err := ValidateMemoryMutation(&mutations[index], characterID); err != nil {
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
	conversationID, batchCharacterID, err := LockRunningExtractionBatch(queryCtx, tx, batchID)
	if err != nil {
		return nil, err
	}
	if batchCharacterID != characterID {
		return nil, errors.New("extraction batch does not belong to character")
	}
	allowedTurnIDs, err := LoadExtractionBatchTurnIDs(queryCtx, tx, batchID)
	if err != nil {
		return nil, err
	}
	now := nowUnixMS()
	results := make([]MemoryMutationResult, 0, len(mutations))
	for _, mutation := range mutations {
		if _, ok := allowedTurnIDs[mutation.SourceTurnID]; !ok {
			return nil, errors.New("memory mutation source turn is not provided to the batch")
		}
		switch mutation.Operation {
		case "create":
			existingID, err := FindDuplicateMemory(queryCtx, tx, mutation.Kind, mutation.Scope, mutation.Content)
			if err != nil {
				return nil, err
			}
			if existingID != "" {
				results = append(results, MemoryMutationResult{Status: "no_change", ExistingMemoryID: existingID})
				continue
			}
			record, err := insertPersonalMemoryPostgres(queryCtx, tx, newID(), mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints, conversationID, mutation.SourceTurnID, nil, now)
			if err != nil {
				return nil, err
			}
			if err := LinkPersonalMemoryEvidence(queryCtx, tx, record.ID, mutation.SourceTurnID, now); err != nil {
				return nil, err
			}
			results = append(results, MemoryMutationResult{Status: "applied", MemoryID: record.ID})
		case "supersede":
			if _, ok := allowed[mutation.MemoryID]; !ok {
				return nil, errors.New("supersede references a memory id not provided to the batch")
			}
			if err := RequireActiveMemoryScope(queryCtx, tx, mutation.MemoryID, mutation.Kind, mutation.Scope); err != nil {
				return nil, err
			}
			existingID, err := FindDuplicateMemory(queryCtx, tx, mutation.Kind, mutation.Scope, mutation.Content)
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
			record, err := insertPersonalMemoryPostgres(queryCtx, tx, newID(), mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints, conversationID, mutation.SourceTurnID, &supersedesID, now)
			if err != nil {
				return nil, err
			}
			if err := LinkPersonalMemoryEvidence(queryCtx, tx, record.ID, mutation.SourceTurnID, now); err != nil {
				return nil, err
			}
			results = append(results, MemoryMutationResult{Status: "applied", MemoryID: record.ID})
		default:
			return nil, fmt.Errorf("unsupported memory mutation operation %q", mutation.Operation)
		}
	}
	if err := SucceedExtractionBatch(queryCtx, tx, batchID, s.workerID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing memory mutations: %w", err)
	}
	return results, nil
}
