package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type FeedbackEventType string

const (
	FeedbackPersonalMemory     FeedbackEventType = "personal_memory"
	FeedbackWebKnowledge       FeedbackEventType = "web_knowledge"
	FeedbackSocialLearning     FeedbackEventType = "social_learning"
	FeedbackSocialReplyOutcome FeedbackEventType = "social_reply_feedback"

	MaxFeedbackEventPayloadBytes = 16 << 10
	MaxFeedbackEventAttempts     = 3
	MaxFeedbackEventsPerClaim    = 100
)

type FeedbackEventInput struct {
	ID             string
	Type           FeedbackEventType
	ConversationID string
	TurnID         string
	CharacterID    string
	Payload        json.RawMessage
	Status         string
}

type FeedbackEventRecord struct {
	ID              string
	Type            FeedbackEventType
	ConversationID  string
	TurnID          string
	CharacterID     string
	Payload         json.RawMessage
	Status          string
	ClaimGroupID    string
	AttemptCount    int
	NextAttemptAtMS int64
	ErrorCategory   string
	ErrorMessage    string
	CreatedAtUnixMS int64
	UpdatedAtUnixMS int64
}

func ValidateFeedbackEventInput(input FeedbackEventInput) error {
	if err := ValidateID("feedback_event_id", input.ID); err != nil {
		return err
	}
	if !validFeedbackEventType(input.Type) {
		return errors.New("feedback event type is unsupported")
	}
	if err := ValidateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := ValidateID("turn_id", input.TurnID); err != nil {
		return err
	}
	if err := ValidateID("character_id", input.CharacterID); err != nil {
		return err
	}
	if input.Status != "waiting_turn" && input.Status != "pending" {
		return errors.New("feedback event initial status is invalid")
	}
	if len(input.Payload) == 0 || len(input.Payload) > MaxFeedbackEventPayloadBytes {
		return errors.New("feedback event payload size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Payload))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		return errors.New("feedback event payload must be one JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("feedback event payload contains trailing JSON")
	}
	return nil
}

func validFeedbackEventType(eventType FeedbackEventType) bool {
	switch eventType {
	case FeedbackPersonalMemory, FeedbackWebKnowledge, FeedbackSocialLearning, FeedbackSocialReplyOutcome:
		return true
	default:
		return false
	}
}

func (s *Store) EnqueueFeedbackEventsContext(ctx context.Context, inputs []FeedbackEventInput) error {
	if len(inputs) == 0 {
		return nil
	}
	for index, input := range inputs {
		if err := ValidateFeedbackEventInput(input); err != nil {
			return fmt.Errorf("validating feedback event[%d]: %w", index, err)
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning feedback event enqueue transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	for _, input := range inputs {
		if _, err := tx.Exec(queryCtx, `
INSERT INTO feedback_events(
  id, type, conversation_id, turn_id, character_id, payload_json,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $8)
ON CONFLICT (id) DO NOTHING`,
			input.ID, string(input.Type), input.ConversationID, input.TurnID,
			input.CharacterID, []byte(input.Payload), input.Status, now,
		); err != nil {
			return fmt.Errorf("enqueueing feedback event: %w", err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing feedback event enqueue: %w", err)
	}
	return nil
}

func (s *Store) ClaimFeedbackEventsContext(ctx context.Context, eventType FeedbackEventType, limit int) ([]FeedbackEventRecord, error) {
	if !validFeedbackEventType(eventType) {
		return nil, errors.New("feedback event type is unsupported")
	}
	if limit < 1 || limit > MaxFeedbackEventsPerClaim {
		return nil, fmt.Errorf("feedback event claim limit must be between 1 and %d", MaxFeedbackEventsPerClaim)
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning feedback event claim transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	if err := settleFeedbackEvents(queryCtx, tx, now); err != nil {
		return nil, err
	}
	rows, err := tx.Query(queryCtx, `
WITH candidates AS (
  SELECT event.id
  FROM feedback_events AS event
  JOIN conversation_turns AS turn ON turn.id = event.turn_id
  WHERE event.type = $1
    AND (
      (event.status IN ('waiting_turn', 'pending')
        AND turn.status = 'completed'
        AND event.next_attempt_at_ms <= $2
        AND event.attempt_count < $3)
      OR
      (event.status = 'running'
        AND event.lease_expires_at_ms <= $2
        AND event.attempt_count < $3)
    )
  ORDER BY event.updated_at_ms, event.id
  LIMIT $4
  FOR UPDATE OF event SKIP LOCKED
)
UPDATE feedback_events AS event
SET status = 'running',
    lease_owner = $5,
    lease_expires_at_ms = $6,
    claim_group_id = event.id,
    attempt_count = event.attempt_count + 1,
    next_attempt_at_ms = 0,
    updated_at_ms = $2
FROM candidates
WHERE event.id = candidates.id
RETURNING event.id, event.type, event.conversation_id, event.turn_id,
          event.character_id, event.payload_json, event.status,
          event.claim_group_id, event.attempt_count, event.next_attempt_at_ms,
          COALESCE(event.error_category, ''), COALESCE(event.error_message, ''),
          event.created_at_ms, event.updated_at_ms`,
		string(eventType), now, MaxFeedbackEventAttempts, limit,
		s.workerID, now+s.jobLeaseDuration.Milliseconds(),
	)
	if err != nil {
		return nil, fmt.Errorf("claiming feedback events: %w", err)
	}
	events, err := scanFeedbackEvents(rows)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing feedback event claim: %w", err)
	}
	return events, nil
}

func (s *Store) ClaimPersonalFeedbackEventsContext(ctx context.Context, conversationID string, limit int) ([]FeedbackEventRecord, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > DefaultExtractionBatchLimit {
		return nil, errors.New("personal feedback event limit must be between 1 and 12")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning personal feedback claim transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	if err := settleFeedbackEvents(queryCtx, tx, now); err != nil {
		return nil, err
	}
	var hasRunning bool
	if err := tx.QueryRow(queryCtx, `
SELECT EXISTS(
  SELECT 1 FROM feedback_events
  WHERE type = 'personal_memory'
    AND conversation_id = $1
    AND status = 'running'
    AND lease_expires_at_ms > $2
)`, conversationID, now).Scan(&hasRunning); err != nil {
		return nil, fmt.Errorf("checking running personal feedback events: %w", err)
	}
	if hasRunning {
		return nil, nil
	}
	groupID := newID()
	rows, err := tx.Query(queryCtx, `
WITH candidates AS (
  SELECT event.id
  FROM feedback_events AS event
  JOIN conversation_turns AS turn ON turn.id = event.turn_id
  WHERE event.type = 'personal_memory'
    AND event.conversation_id = $1
    AND turn.status = 'completed'
    AND (
      (event.status IN ('waiting_turn', 'pending')
        AND event.next_attempt_at_ms <= $2
        AND event.attempt_count < $3)
      OR
      (event.status = 'running'
        AND event.lease_expires_at_ms <= $2
        AND event.attempt_count < $3)
    )
  ORDER BY event.created_at_ms, event.id
  LIMIT $4
  FOR UPDATE OF event SKIP LOCKED
)
UPDATE feedback_events AS event
SET status = 'running',
    lease_owner = $5,
    lease_expires_at_ms = $6,
    claim_group_id = $7,
    attempt_count = event.attempt_count + 1,
    next_attempt_at_ms = 0,
    updated_at_ms = $2
FROM candidates
WHERE event.id = candidates.id
RETURNING event.id, event.type, event.conversation_id, event.turn_id,
          event.character_id, event.payload_json, event.status,
          event.claim_group_id, event.attempt_count, event.next_attempt_at_ms,
          COALESCE(event.error_category, ''), COALESCE(event.error_message, ''),
          event.created_at_ms, event.updated_at_ms`,
		conversationID, now, MaxFeedbackEventAttempts, limit, s.workerID,
		now+s.jobLeaseDuration.Milliseconds(), groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("claiming personal feedback events: %w", err)
	}
	events, err := scanFeedbackEvents(rows)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing personal feedback event claim: %w", err)
	}
	return events, nil
}

func settleFeedbackEvents(ctx context.Context, tx pgx.Tx, now int64) error {
	if _, err := tx.Exec(ctx, `
UPDATE feedback_events AS event
SET status = 'dropped',
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = 0,
    error_category = 'turn_not_completed',
    error_message = 'source turn did not complete',
    updated_at_ms = $1
FROM conversation_turns AS turn
WHERE turn.id = event.turn_id
  AND event.status = 'waiting_turn'
  AND turn.status IN ('failed', 'interrupted')`, now); err != nil {
		return fmt.Errorf("dropping feedback events for unsuccessful turns: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE feedback_events
SET status = 'failed',
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = 0,
    error_category = 'attempts_exhausted',
    error_message = 'feedback event lease expired after maximum attempts',
    updated_at_ms = $1
WHERE status = 'running'
  AND lease_expires_at_ms <= $1
  AND attempt_count >= $2`, now, MaxFeedbackEventAttempts); err != nil {
		return fmt.Errorf("failing exhausted feedback events: %w", err)
	}
	return nil
}

func scanFeedbackEvents(rows pgx.Rows) ([]FeedbackEventRecord, error) {
	defer rows.Close()
	events := make([]FeedbackEventRecord, 0)
	for rows.Next() {
		var event FeedbackEventRecord
		var payload []byte
		if err := rows.Scan(
			&event.ID, &event.Type, &event.ConversationID, &event.TurnID,
			&event.CharacterID, &payload, &event.Status, &event.ClaimGroupID,
			&event.AttemptCount, &event.NextAttemptAtMS, &event.ErrorCategory,
			&event.ErrorMessage, &event.CreatedAtUnixMS, &event.UpdatedAtUnixMS,
		); err != nil {
			return nil, fmt.Errorf("scanning feedback event: %w", err)
		}
		event.Payload = append(json.RawMessage(nil), payload...)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating feedback events: %w", err)
	}
	slices.SortFunc(events, func(left, right FeedbackEventRecord) int {
		if left.CreatedAtUnixMS != right.CreatedAtUnixMS {
			if left.CreatedAtUnixMS < right.CreatedAtUnixMS {
				return -1
			}
			return 1
		}
		return strings.Compare(left.ID, right.ID)
	})
	return events, nil
}
