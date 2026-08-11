package extraction

import (
	"context"
	"errors"
	"fairy/context/memory/personal"
	"fairy/runtime/embedding"
	"fmt"
)

func (s *Store) commitMemoryMutationsPostgres(ctx context.Context, batchID, characterID string, allowedMemoryIDs []string, mutations []Mutation) ([]MutationResult, error) {
	if err := validateID("batch_id", batchID); err != nil {
		return nil, err
	}
	if err := validateID("character_id", characterID); err != nil {
		return nil, err
	}
	if len(mutations) > MaxMutations {
		return nil, errors.New("extraction batch exceeds memory mutation limit")
	}
	for index := range mutations {
		if err := ValidateMutation(&mutations[index], characterID); err != nil {
			return nil, err
		}
	}
	allowed := make(map[string]struct{}, len(allowedMemoryIDs))
	for _, id := range allowedMemoryIDs {
		allowed[id] = struct{}{}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	embeddings := make([]embedding.EmbeddingValue, len(mutations))
	embeddingPrepared := make([]bool, len(mutations))
	byContent := make(map[string]embedding.EmbeddingValue, len(mutations))
	for index, mutation := range mutations {
		if mutation.Operation != OperationAdd && mutation.Operation != OperationReplace {
			continue
		}
		existingID, err := personal.FindActiveDuplicate(queryCtx, s.pool.Raw(), mutation.Kind, mutation.Scope, mutation.Content)
		if err != nil {
			return nil, err
		}
		if existingID != "" && (mutation.Operation == OperationAdd || existingID != mutation.MemoryID) {
			continue
		}
		embedding, ok := byContent[mutation.Content]
		if !ok {
			embedding, err = s.embeddingForContent(mutation.Content)
			if err != nil {
				return nil, err
			}
			byContent[mutation.Content] = embedding
		}
		embeddings[index] = embedding
		embeddingPrepared[index] = true
	}
	tx, err := s.pool.Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning memory mutation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	conversationID, batchCharacterID, err := LockRunningBatch(queryCtx, tx, batchID, s.workerID)
	if err != nil {
		return nil, err
	}
	if batchCharacterID != characterID {
		return nil, errors.New("extraction batch does not belong to character")
	}
	allowedTurnIDs, err := LoadBatchTurnIDs(queryCtx, tx, batchID)
	if err != nil {
		return nil, err
	}
	now := nowUnixMS()
	results := make([]MutationResult, 0, len(mutations))
	for index, mutation := range mutations {
		if _, ok := allowedTurnIDs[mutation.SourceTurnID]; !ok {
			return nil, errors.New("memory mutation source turn is not provided to the batch")
		}
		switch mutation.Operation {
		case OperationAdd:
			existingID, err := personal.FindActiveDuplicate(queryCtx, tx, mutation.Kind, mutation.Scope, mutation.Content)
			if err != nil {
				return nil, err
			}
			if existingID != "" {
				results = append(results, MutationResult{Status: "no_change", ExistingMemoryID: existingID})
				continue
			}
			if !embeddingPrepared[index] {
				return nil, errors.New("memory candidates changed during embedding preparation")
			}
			embedding := embeddings[index]
			record, err := personal.Insert(queryCtx, tx, newID(), mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints, conversationID, mutation.SourceTurnID, nil, now, embedding)
			if err != nil {
				return nil, err
			}
			evidenceIDs, err := LoadTurnEvidenceIDs(queryCtx, tx, mutation.SourceTurnID)
			if err != nil {
				return nil, err
			}
			if err := personal.SetEvidenceIDs(queryCtx, tx, record.ID, evidenceIDs, now); err != nil {
				return nil, err
			}
			results = append(results, MutationResult{Status: "applied", MemoryID: record.ID})
		case OperationReplace:
			if _, ok := allowed[mutation.MemoryID]; !ok {
				return nil, errors.New("REPLACE references a memory id not provided to the batch")
			}
			if err := personal.RequireActiveScope(queryCtx, tx, mutation.MemoryID, mutation.Kind, mutation.Scope); err != nil {
				return nil, err
			}
			existingID, err := personal.FindActiveDuplicate(queryCtx, tx, mutation.Kind, mutation.Scope, mutation.Content)
			if err != nil {
				return nil, err
			}
			if existingID != "" && existingID != mutation.MemoryID {
				results = append(results, MutationResult{Status: "no_change", ExistingMemoryID: existingID})
				continue
			}
			if !embeddingPrepared[index] {
				return nil, errors.New("memory candidates changed during embedding preparation")
			}
			if err := personal.Supersede(queryCtx, tx, mutation.MemoryID, now); err != nil {
				return nil, err
			}
			supersedesID := mutation.MemoryID
			embedding := embeddings[index]
			record, err := personal.Insert(queryCtx, tx, newID(), mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints, conversationID, mutation.SourceTurnID, &supersedesID, now, embedding)
			if err != nil {
				return nil, err
			}
			evidenceIDs, err := LoadTurnEvidenceIDs(queryCtx, tx, mutation.SourceTurnID)
			if err != nil {
				return nil, err
			}
			if err := personal.SetEvidenceIDs(queryCtx, tx, record.ID, evidenceIDs, now); err != nil {
				return nil, err
			}
			results = append(results, MutationResult{Status: "applied", MemoryID: record.ID})
		case OperationDelete:
			if _, ok := allowed[mutation.MemoryID]; !ok {
				return nil, errors.New("DELETE references a memory id not provided to the batch")
			}
			if err := personal.RequireActive(queryCtx, tx, mutation.MemoryID); err != nil {
				return nil, err
			}
			if err := personal.Tombstone(queryCtx, tx, mutation.MemoryID, now); err != nil {
				return nil, err
			}
			results = append(results, MutationResult{Status: "applied", ExistingMemoryID: mutation.MemoryID})
		case OperationNone:
			if _, ok := allowed[mutation.MemoryID]; !ok {
				return nil, errors.New("NONE references a memory id not provided to the batch")
			}
			if err := personal.RequireActive(queryCtx, tx, mutation.MemoryID); err != nil {
				return nil, fmt.Errorf("reading NONE target personal memory: %w", err)
			}
			results = append(results, MutationResult{Status: "no_change", ExistingMemoryID: mutation.MemoryID})
		default:
			return nil, fmt.Errorf("unsupported memory mutation operation %q", mutation.Operation)
		}
	}
	for index, result := range results {
		memoryID := result.MemoryID
		if memoryID == "" {
			memoryID = result.ExistingMemoryID
		}
		if memoryID == "" {
			return nil, errors.New("committed memory mutation has no coverage memory")
		}
		if err := InsertCoverage(
			queryCtx, tx, conversationID, mutations[index].SourceTurnID,
			memoryID, result.Status, now,
		); err != nil {
			return nil, err
		}
	}
	if err := SucceedBatch(queryCtx, tx, batchID, s.workerID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing memory mutations: %w", err)
	}
	return results, nil
}
