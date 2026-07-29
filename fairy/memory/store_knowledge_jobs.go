package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) KnowledgeIngestJobs(status string) ([]KnowledgeIngestJobRecord, error) {
	return s.KnowledgeIngestJobsContext(context.Background(), status)
}

func (s *Store) KnowledgeIngestJobsContext(ctx context.Context, status string) ([]KnowledgeIngestJobRecord, error) {
	status = strings.TrimSpace(status)
	if status != "" {
		switch status {
		case "waiting_turn", "pending", "running", "succeeded", "failed", "dropped":
		default:
			return nil, errors.New("knowledge ingest job status is invalid")
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT id, conversation_id, turn_id, task_id, status, attempt_count,
       next_attempt_at_ms, COALESCE(error_category, ''), COALESCE(error_message, ''),
       created_at_ms, updated_at_ms
FROM knowledge_ingest_jobs
WHERE ($1 = '' OR status = $1)
ORDER BY updated_at_ms DESC, id ASC
LIMIT 100`, status)
	if err != nil {
		return nil, fmt.Errorf("listing knowledge ingest jobs: %w", err)
	}
	defer rows.Close()
	records := make([]KnowledgeIngestJobRecord, 0)
	for rows.Next() {
		var record KnowledgeIngestJobRecord
		if err := rows.Scan(
			&record.ID, &record.ConversationID, &record.TurnID, &record.TaskID,
			&record.Status, &record.AttemptCount, &record.NextAttemptAtMS,
			&record.ErrorCategory, &record.ErrorMessage, &record.CreatedAtMS, &record.UpdatedAtMS,
		); err != nil {
			return nil, fmt.Errorf("scanning knowledge ingest job: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge ingest jobs: %w", err)
	}
	return records, nil
}

func (s *Store) RetryKnowledgeIngestJob(id string) error {
	return s.RetryKnowledgeIngestJobContext(context.Background(), id)
}

func (s *Store) RetryKnowledgeIngestJobContext(ctx context.Context, id string) error {
	if err := ValidateID("knowledge_ingest_job_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	now := nowUnixMS()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE knowledge_ingest_jobs j
SET status = 'pending', attempt_count = 0, next_attempt_at_ms = 0,
    lease_owner = NULL, lease_expires_at_ms = NULL,
    error_category = NULL, error_message = NULL, updated_at_ms = $2
FROM conversation_turns t
WHERE j.id = $1
  AND t.id = j.turn_id
  AND t.status = 'completed'
  AND j.task_id <> ''
  AND j.status IN ('failed', 'dropped')`, id, now)
	if err != nil {
		return fmt.Errorf("retrying knowledge ingest job: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest job is not retryable")
	}
	return nil
}

func (s *Store) DropKnowledgeIngestJob(id string) error {
	return s.DropKnowledgeIngestJobContext(context.Background(), id)
}

func (s *Store) DropKnowledgeIngestJobContext(ctx context.Context, id string) error {
	if err := ValidateID("knowledge_ingest_job_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE knowledge_ingest_jobs
SET status = 'dropped', next_attempt_at_ms = 0,
    lease_owner = NULL, lease_expires_at_ms = NULL,
    error_category = 'manual_drop', error_message = 'dropped by operator',
    updated_at_ms = $2
WHERE id = $1 AND status IN ('waiting_turn', 'pending', 'failed')`, id, nowUnixMS())
	if err != nil {
		return fmt.Errorf("dropping knowledge ingest job: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest job is not droppable")
	}
	return nil
}
