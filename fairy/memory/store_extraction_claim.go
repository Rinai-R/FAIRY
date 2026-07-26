package memory

import (
	"context"
	"errors"
	"fmt"
)

func (s *Store) claimExtractionBatchPostgres(ctx context.Context, conversationID string, limit int) (*ExtractionBatchInput, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > DefaultExtractionBatchLimit {
		return nil, errors.New("extraction batch limit must be between 1 and 12")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning extraction claim transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	characterID, err := LockConversationCharacter(queryCtx, tx, conversationID)
	if err != nil {
		return nil, err
	}
	now := nowUnixMS()
	leaseExpires := now + s.jobLeaseDuration.Milliseconds()
	reclaimedBatchID, err := ReclaimExpiredExtractionBatch(queryCtx, tx, conversationID, s.workerID, now, leaseExpires)
	if err != nil {
		return nil, err
	}
	if reclaimedBatchID != "" {
		input, err := LoadExtractionBatchInput(queryCtx, tx, reclaimedBatchID, conversationID, characterID, normalizePostgresSearchQuery)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(queryCtx); err != nil {
			return nil, fmt.Errorf("committing extraction reclaim: %w", err)
		}
		return input, nil
	}
	running, err := HasRunningExtractionBatch(queryCtx, tx, conversationID)
	if err != nil {
		return nil, err
	}
	if running {
		return nil, nil
	}
	claimed, err := SelectPendingExtractionTurns(queryCtx, tx, conversationID, limit)
	if err != nil {
		return nil, err
	}
	if len(claimed) == 0 {
		return nil, nil
	}
	batchID := newID()
	first := claimed[0].Sequence
	last := claimed[len(claimed)-1].Sequence
	if err := InsertExtractionBatch(queryCtx, tx, batchID, conversationID, characterID, s.workerID, first, last, leaseExpires, now); err != nil {
		return nil, err
	}
	if err := ClaimExtractionTurns(queryCtx, tx, batchID, conversationID, now, claimed); err != nil {
		return nil, err
	}
	input, err := BuildExtractionBatchInput(queryCtx, tx, batchID, conversationID, characterID, claimed, normalizePostgresSearchQuery)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing extraction claim: %w", err)
	}
	return input, nil
}

func (s *Store) pendingExtractionTurnCountPostgres(ctx context.Context, conversationID string) (uint64, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return 0, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	count, err := CountPendingExtractionTurns(queryCtx, s.pool.Raw(), conversationID)
	if err != nil {
		return 0, err
	}
	return uint64(count), nil
}

func (s *Store) failExtractionBatchPostgres(ctx context.Context, batchID, code, message string, retryable bool) error {
	if err := ValidateID("batch_id", batchID); err != nil {
		return err
	}
	if code == "" || message == "" {
		return errors.New("extraction failure code and message are required")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return FailExtractionBatch(queryCtx, s.pool.Raw(), batchID, s.workerID, code, message, retryable, nowUnixMS())
}

func (s *Store) completeExtractionBatchPostgres(ctx context.Context, batchID string) error {
	if err := ValidateID("batch_id", batchID); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning extraction completion transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := SucceedExtractionBatch(queryCtx, tx, batchID, s.workerID, nowUnixMS()); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing extraction completion transaction: %w", err)
	}
	return nil
}

func retrievePersonalTrigramPostgres(ctx context.Context, db Querier, characterID, query string, remaining *int) ([]RetrievedPersonalMemory, error) {
	return RetrievePersonalTrigram(ctx, db, characterID, query, remaining)
}
