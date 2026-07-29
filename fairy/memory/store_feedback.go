package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) RenewFeedbackEventGroupContext(ctx context.Context, groupID string) error {
	if err := ValidateID("feedback_event_group_id", groupID); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	now := nowUnixMS()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE feedback_events
SET lease_expires_at_ms = $3, updated_at_ms = $4
WHERE claim_group_id = $1 AND status = 'running' AND lease_owner = $2`,
		groupID, s.workerID, now+s.jobLeaseDuration.Milliseconds(), now,
	)
	if err != nil {
		return fmt.Errorf("renewing feedback event lease: %w", err)
	}
	if changed.RowsAffected() == 0 {
		return errors.New("feedback event group is not owned by this worker")
	}
	return nil
}

func (s *Store) CompleteFeedbackEventGroupContext(ctx context.Context, groupID string) error {
	return s.finishFeedbackEventGroup(ctx, groupID, "succeeded", "", "")
}

func (s *Store) DropFeedbackEventGroupContext(ctx context.Context, groupID, message string) error {
	return s.finishFeedbackEventGroup(ctx, groupID, "dropped", "turn_not_completed", message)
}

func (s *Store) FailFeedbackEventGroupContext(ctx context.Context, groupID, category, message string) error {
	category = strings.TrimSpace(category)
	if category == "" {
		category = "terminal"
	}
	return s.finishFeedbackEventGroup(ctx, groupID, "failed", category, message)
}

func (s *Store) finishFeedbackEventGroup(ctx context.Context, groupID, status, category, message string) error {
	if err := ValidateID("feedback_event_group_id", groupID); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE feedback_events
SET status = $3,
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = 0,
    error_category = NULLIF($4, ''),
    error_message = NULLIF($5, ''),
    updated_at_ms = $6
WHERE claim_group_id = $1 AND status = 'running' AND lease_owner = $2`,
		groupID, s.workerID, status, category, CleanEmbeddingErrorMessage(message), nowUnixMS(),
	)
	if err != nil {
		return fmt.Errorf("finishing feedback event group: %w", err)
	}
	if changed.RowsAffected() == 0 {
		return errors.New("feedback event group is not owned by this worker")
	}
	return nil
}

func (s *Store) RetryFeedbackEventGroupContext(ctx context.Context, groupID, category, message string) error {
	if err := ValidateID("feedback_event_group_id", groupID); err != nil {
		return err
	}
	category = strings.TrimSpace(category)
	if category == "" {
		return errors.New("feedback event retry category is required")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	now := nowUnixMS()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE feedback_events
SET status = CASE WHEN attempt_count >= $4 THEN 'failed' ELSE 'pending' END,
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = CASE
      WHEN attempt_count >= $4 THEN 0
      ELSE $5::bigint + LEAST(30000::bigint, 1000::bigint * (1::bigint << GREATEST(0, attempt_count - 1)))
    END,
    error_category = $2,
    error_message = NULLIF($3, ''),
    updated_at_ms = $5
WHERE claim_group_id = $1 AND status = 'running' AND lease_owner = $6`,
		groupID, category, CleanEmbeddingErrorMessage(message), MaxFeedbackEventAttempts, now, s.workerID,
	)
	if err != nil {
		return fmt.Errorf("retrying feedback event group: %w", err)
	}
	if changed.RowsAffected() == 0 {
		return errors.New("feedback event group is not owned by this worker")
	}
	return nil
}

func (s *Store) ReleaseFeedbackEventGroupContext(ctx context.Context, groupID string) error {
	if err := ValidateID("feedback_event_group_id", groupID); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE feedback_events
SET status = 'pending',
    attempt_count = GREATEST(0, attempt_count - 1),
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = 0,
    updated_at_ms = $3
WHERE claim_group_id = $1 AND status = 'running' AND lease_owner = $2`,
		groupID, s.workerID, nowUnixMS(),
	)
	if err != nil {
		return fmt.Errorf("releasing feedback event group: %w", err)
	}
	if changed.RowsAffected() == 0 {
		return errors.New("feedback event group is not owned by this worker")
	}
	return nil
}

func (s *Store) RetryFailedFeedbackEventContext(ctx context.Context, eventID string) error {
	if err := ValidateID("feedback_event_id", eventID); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE feedback_events
SET status = 'pending',
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    attempt_count = 0,
    next_attempt_at_ms = 0,
    error_category = NULL,
    error_message = NULL,
    updated_at_ms = $2
WHERE id = $1 AND status = 'failed'`, eventID, nowUnixMS())
	if err != nil {
		return fmt.Errorf("retrying failed feedback event: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("feedback event is not failed")
	}
	return nil
}

func (s *Store) LoadOwnedFeedbackEventGroupContext(ctx context.Context, groupID string) ([]FeedbackEventRecord, error) {
	if err := ValidateID("feedback_event_group_id", groupID); err != nil {
		return nil, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT id, type, conversation_id, turn_id, character_id, payload_json, status,
       claim_group_id, attempt_count, next_attempt_at_ms,
       COALESCE(error_category, ''), COALESCE(error_message, ''),
       created_at_ms, updated_at_ms
FROM feedback_events
WHERE claim_group_id = $1 AND status = 'running' AND lease_owner = $2
ORDER BY created_at_ms, id`, groupID, s.workerID)
	if err != nil {
		return nil, fmt.Errorf("loading owned feedback event group: %w", err)
	}
	events, err := scanFeedbackEvents(rows)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, errors.New("feedback event group is not owned by this worker")
	}
	return events, nil
}
