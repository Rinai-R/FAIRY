package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const maxExtractionAttempts = 3

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
	now := nowUnixMS()
	if _, err := tx.Exec(queryCtx, `
UPDATE conversation_turns
SET extraction_state = CASE WHEN extraction_attempt_count >= $2 THEN 'failed' ELSE 'pending' END,
    extraction_claim_id = NULL,
    extraction_lease_owner = NULL,
    extraction_lease_expires_at_ms = NULL,
    extraction_next_attempt_at_ms = 0,
    extraction_error_code = CASE WHEN extraction_attempt_count >= $2 THEN 'lease_expired' ELSE extraction_error_code END,
    extraction_error_message = CASE WHEN extraction_attempt_count >= $2 THEN 'extraction lease expired after maximum attempts' ELSE extraction_error_message END,
    updated_at_ms = $1
WHERE status = 'completed'
  AND extraction_state = 'claimed'
  AND extraction_lease_expires_at_ms <= $1`, now, maxExtractionAttempts); err != nil {
		return nil, fmt.Errorf("recovering expired extraction claims: %w", err)
	}
	var hasRunning bool
	if err := tx.QueryRow(queryCtx, `
SELECT EXISTS(
  SELECT 1 FROM conversation_turns
  WHERE conversation_id = $1
    AND status = 'completed'
    AND extraction_state = 'claimed'
    AND extraction_lease_expires_at_ms > $2
)`, conversationID, now).Scan(&hasRunning); err != nil {
		return nil, fmt.Errorf("checking running extraction claim: %w", err)
	}
	if hasRunning {
		return nil, nil
	}
	characterID, err := LockConversationCharacter(queryCtx, tx, conversationID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(queryCtx, `
SELECT id
FROM conversation_turns
WHERE conversation_id = $1
  AND status = 'completed'
  AND extraction_state = 'pending'
  AND extraction_next_attempt_at_ms <= $2
  AND extraction_attempt_count < $3
ORDER BY sequence
LIMIT $4
FOR UPDATE SKIP LOCKED`, conversationID, now, maxExtractionAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("selecting extraction turns: %w", err)
	}
	turnIDs := make([]string, 0, limit)
	for rows.Next() {
		var turnID string
		if err := rows.Scan(&turnID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning extraction turn id: %w", err)
		}
		turnIDs = append(turnIDs, turnID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating extraction turn ids: %w", err)
	}
	rows.Close()
	if len(turnIDs) == 0 {
		return nil, nil
	}
	claimID := newID()
	leaseExpires := now + s.jobLeaseDuration.Milliseconds()
	changed, err := tx.Exec(queryCtx, `
UPDATE conversation_turns
SET extraction_state = 'claimed',
    extraction_claim_id = $2,
    extraction_lease_owner = $3,
    extraction_lease_expires_at_ms = $4,
    extraction_attempt_count = extraction_attempt_count + 1,
    extraction_next_attempt_at_ms = 0,
    extraction_error_code = NULL,
    extraction_error_message = NULL,
    updated_at_ms = $5
WHERE id = ANY($1)
  AND extraction_state = 'pending'`, turnIDs, claimID, s.workerID, leaseExpires, now)
	if err != nil {
		return nil, fmt.Errorf("claiming extraction turns: %w", err)
	}
	if changed.RowsAffected() != int64(len(turnIDs)) {
		return nil, errors.New("extraction turns changed during claim")
	}
	messageRows, err := tx.Query(queryCtx, `
SELECT turn.id, turn.sequence, user_message.content, assistant_message.content
FROM conversation_turns AS turn
JOIN conversation_messages AS user_message
  ON user_message.turn_id = turn.id AND user_message.role = 'user'
JOIN conversation_messages AS assistant_message
  ON assistant_message.turn_id = turn.id AND assistant_message.role = 'assistant'
WHERE turn.extraction_claim_id = $1
  AND turn.extraction_lease_owner = $2
  AND turn.extraction_state = 'claimed'
ORDER BY turn.sequence`, claimID, s.workerID)
	if err != nil {
		return nil, fmt.Errorf("loading claimed extraction turns: %w", err)
	}
	claimed := make([]ExtractionTurnRow, 0, len(turnIDs))
	for messageRows.Next() {
		var item ExtractionTurnRow
		if err := messageRows.Scan(&item.ID, &item.Sequence, &item.User, &item.Assistant); err != nil {
			messageRows.Close()
			return nil, fmt.Errorf("scanning claimed extraction turn: %w", err)
		}
		claimed = append(claimed, item)
	}
	if err := messageRows.Err(); err != nil {
		messageRows.Close()
		return nil, fmt.Errorf("iterating claimed extraction turns: %w", err)
	}
	messageRows.Close()
	if len(claimed) != len(turnIDs) {
		return nil, errors.New("extraction claim is missing completed turn messages")
	}
	input, err := BuildExtractionBatchInput(
		queryCtx, tx, claimID, conversationID, characterID, claimed,
		normalizePostgresSearchQuery,
	)
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
	var count int64
	if err := s.pool.Raw().QueryRow(queryCtx, `
SELECT count(*)
FROM conversation_turns
WHERE conversation_id = $1
  AND status = 'completed'
  AND extraction_state = 'pending'`, conversationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting pending extraction turns: %w", err)
	}
	if count < 0 {
		return 0, errors.New("pending extraction turn count is invalid")
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
	now := nowUnixMS()
	status := "failed"
	if retryable {
		status = "pending"
	}
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE conversation_turns
SET extraction_state = CASE
      WHEN $4 = 'pending' AND extraction_attempt_count < $5 THEN 'pending'
      ELSE 'failed'
    END,
    extraction_claim_id = NULL,
    extraction_lease_owner = NULL,
    extraction_lease_expires_at_ms = NULL,
    extraction_next_attempt_at_ms = CASE
      WHEN $4 = 'pending' AND extraction_attempt_count < $5
      THEN $6::bigint + LEAST(30000::bigint, 1000::bigint * (1::bigint << GREATEST(0, extraction_attempt_count - 1)))
      ELSE 0
    END,
    extraction_error_code = $2,
    extraction_error_message = $3,
    updated_at_ms = $6
WHERE extraction_claim_id = $1
  AND extraction_state = 'claimed'
  AND extraction_lease_owner = $7`,
		batchID, code, CleanEmbeddingErrorMessage(message), status,
		maxExtractionAttempts, now, s.workerID)
	if err != nil {
		return fmt.Errorf("failing extraction claim: %w", err)
	}
	if changed.RowsAffected() == 0 {
		return errors.New("extraction claim is not owned by this worker")
	}
	return nil
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
	if err := succeedExtractionClaim(queryCtx, tx, batchID, s.workerID, nowUnixMS()); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing extraction completion transaction: %w", err)
	}
	return nil
}

func succeedExtractionClaim(ctx context.Context, tx pgx.Tx, claimID, workerID string, now int64) error {
	changed, err := tx.Exec(ctx, `
UPDATE conversation_turns
SET extraction_state = 'processed',
    extraction_claim_id = NULL,
    extraction_lease_owner = NULL,
    extraction_lease_expires_at_ms = NULL,
    extraction_next_attempt_at_ms = 0,
    extraction_error_code = NULL,
    extraction_error_message = NULL,
    updated_at_ms = $3
WHERE extraction_claim_id = $1
  AND extraction_state = 'claimed'
  AND extraction_lease_owner = $2`, claimID, workerID, now)
	if err != nil {
		return fmt.Errorf("completing extraction claim: %w", err)
	}
	if changed.RowsAffected() == 0 {
		return errors.New("extraction claim is not owned by this worker")
	}
	return nil
}

func retrievePersonalTrigramPostgres(ctx context.Context, db Querier, characterID, query string, remaining *int) ([]RetrievedPersonalMemory, error) {
	return RetrievePersonalTrigram(ctx, db, characterID, query, remaining)
}
