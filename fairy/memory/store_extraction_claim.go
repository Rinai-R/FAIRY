package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Store) claimExtractionBatchPostgres(ctx context.Context, conversationID string, limit int) (*ExtractionBatchInput, error) {
	events, err := s.ClaimPersonalFeedbackEventsContext(ctx, conversationID, limit)
	if err != nil || len(events) == 0 {
		return nil, err
	}
	groupID := events[0].ClaimGroupID
	characterID := events[0].CharacterID
	turnIDs := make([]string, len(events))
	for index, event := range events {
		if event.ClaimGroupID != groupID || event.ConversationID != conversationID || event.CharacterID != characterID {
			return nil, errors.New("personal feedback claim scope is inconsistent")
		}
		turnIDs[index] = event.TurnID
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning extraction input transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	rows, err := tx.Query(queryCtx, `
SELECT turn.id, turn.sequence, user_message.content, assistant_message.content
FROM conversation_turns AS turn
JOIN conversation_messages AS user_message
  ON user_message.turn_id = turn.id AND user_message.role = 'user'
JOIN conversation_messages AS assistant_message
  ON assistant_message.turn_id = turn.id AND assistant_message.role = 'assistant'
WHERE turn.id = ANY($1)
  AND turn.conversation_id = $2
  AND turn.status = 'completed'
ORDER BY turn.sequence`, turnIDs, conversationID)
	if err != nil {
		return nil, fmt.Errorf("loading personal feedback turns: %w", err)
	}
	claimed := make([]ExtractionTurnRow, 0, len(events))
	for rows.Next() {
		var item ExtractionTurnRow
		if err := rows.Scan(&item.ID, &item.Sequence, &item.User, &item.Assistant); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning personal feedback turn: %w", err)
		}
		claimed = append(claimed, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating personal feedback turns: %w", err)
	}
	rows.Close()
	if len(claimed) != len(events) {
		return nil, errors.New("personal feedback claim is missing completed turn messages")
	}
	if _, err := tx.Exec(queryCtx, `
UPDATE conversation_turns
SET extraction_state = 'claimed', updated_at_ms = $2
WHERE id = ANY($1) AND extraction_state IN ('pending', 'claimed')`, turnIDs, nowUnixMS()); err != nil {
		return nil, fmt.Errorf("marking personal feedback turns claimed: %w", err)
	}
	input, err := BuildExtractionBatchInput(
		queryCtx, tx, groupID, conversationID, characterID, claimed,
		normalizePostgresSearchQuery,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing extraction input transaction: %w", err)
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
FROM feedback_events AS event
JOIN conversation_turns AS turn ON turn.id = event.turn_id
WHERE event.type = 'personal_memory'
  AND event.conversation_id = $1
  AND event.status IN ('waiting_turn', 'pending')
  AND turn.status = 'completed'`, conversationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting pending personal feedback events: %w", err)
	}
	if count < 0 {
		return 0, errors.New("pending personal feedback event count is invalid")
	}
	return uint64(count), nil
}

func (s *Store) failExtractionBatchPostgres(ctx context.Context, batchID, code, message string, retryable bool) error {
	if code == "" || message == "" {
		return errors.New("extraction failure code and message are required")
	}
	if retryable {
		return s.RetryFeedbackEventGroupContext(ctx, batchID, code, message)
	}
	return s.FailFeedbackEventGroupContext(ctx, batchID, code, message)
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
	if err := succeedFeedbackExtractionGroup(queryCtx, tx, batchID, s.workerID, nowUnixMS()); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing extraction completion transaction: %w", err)
	}
	return nil
}

func succeedFeedbackExtractionGroup(ctx context.Context, tx pgx.Tx, groupID, workerID string, now int64) error {
	changed, err := tx.Exec(ctx, `
WITH owned AS (
  SELECT turn_id
  FROM feedback_events
  WHERE claim_group_id = $1 AND status = 'running' AND lease_owner = $2
  FOR UPDATE
),
processed AS (
  UPDATE conversation_turns
  SET extraction_state = 'processed', updated_at_ms = $3
  WHERE id IN (SELECT turn_id FROM owned)
    AND extraction_state = 'claimed'
)
UPDATE feedback_events
SET status = 'succeeded',
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = 0,
    error_category = NULL,
    error_message = NULL,
    updated_at_ms = $3
WHERE claim_group_id = $1 AND status = 'running' AND lease_owner = $2`,
		groupID, workerID, now)
	if err != nil {
		return fmt.Errorf("completing personal feedback events: %w", err)
	}
	if changed.RowsAffected() == 0 {
		return errors.New("personal feedback event group is not owned by this worker")
	}
	return nil
}

func retrievePersonalTrigramPostgres(ctx context.Context, db Querier, characterID, query string, remaining *int) ([]RetrievedPersonalMemory, error) {
	return RetrievePersonalTrigram(ctx, db, characterID, query, remaining)
}
