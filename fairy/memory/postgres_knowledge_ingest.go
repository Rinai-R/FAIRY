package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxKnowledgeIngestJobsPerPass = 100
	MaxKnowledgeIngestAttempts    = MaxFeedbackEventAttempts
)

type KnowledgeIngestJob struct {
	ID             string
	ConversationID string
	TurnID         string
	TaskID         string
	SourceJSON     []byte
	AttemptCount   int
	NextAttemptAt  int64
	ErrorCategory  string
}

// EnqueueKnowledgeIngestTask keeps the existing worker-facing API while storing
// the task as one typed feedback event. Character scope comes from the
// conversation and cannot be forged by the caller.
func EnqueueKnowledgeIngestTask(
	ctx context.Context,
	tx pgx.Tx,
	id, conversationID, turnID, taskID string,
	sourceJSON []byte,
	now int64,
) error {
	changed, err := tx.Exec(ctx, `
INSERT INTO feedback_events(
  id, type, conversation_id, turn_id, character_id, payload_json,
  status, created_at_ms, updated_at_ms
)
SELECT $1, 'web_knowledge', conversation.id, $3, conversation.character_id,
       jsonb_build_object('taskId', $4::text, 'source', $5::jsonb),
       'waiting_turn', $6, $6
FROM conversations AS conversation
JOIN conversation_turns AS turn
  ON turn.id = $3 AND turn.conversation_id = conversation.id
WHERE conversation.id = $2
ON CONFLICT (id) DO NOTHING`,
		id, conversationID, turnID, taskID, sourceJSON, now,
	)
	if err != nil {
		return fmt.Errorf("queueing knowledge ingest feedback: %w", err)
	}
	if changed.RowsAffected() != 1 {
		var exists bool
		if err := tx.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM feedback_events
  WHERE id = $1 AND type = 'web_knowledge'
    AND conversation_id = $2 AND turn_id = $3
    AND payload_json->>'taskId' = $4
)`, id, conversationID, turnID, taskID).Scan(&exists); err != nil {
			return fmt.Errorf("checking knowledge ingest feedback: %w", err)
		}
		if !exists {
			return errors.New("knowledge ingest task scope does not exist")
		}
	}
	return nil
}

type knowledgeIngestQuerier interface {
	Querier
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func ClaimKnowledgeIngestJobs(ctx context.Context, db knowledgeIngestQuerier, limit int, now int64, workerID string, leaseExpires int64) ([]KnowledgeIngestJob, error) {
	if _, err := db.Exec(ctx, `
UPDATE feedback_events
SET status = 'failed',
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = 0,
    error_category = 'attempts_exhausted',
    error_message = 'knowledge ingest lease expired after maximum attempts',
    updated_at_ms = $1
WHERE type = 'web_knowledge'
  AND status = 'running'
  AND lease_expires_at_ms <= $1
  AND attempt_count >= $2`, now, MaxKnowledgeIngestAttempts); err != nil {
		return nil, fmt.Errorf("failing exhausted knowledge ingest feedback: %w", err)
	}
	if _, err := db.Exec(ctx, `
UPDATE feedback_events AS event
SET status = 'dropped',
    error_category = 'turn_not_completed',
    error_message = 'source turn did not complete',
    updated_at_ms = $1
FROM conversation_turns AS turn
WHERE event.type = 'web_knowledge'
  AND turn.id = event.turn_id
  AND event.status = 'waiting_turn'
  AND turn.status IN ('failed', 'interrupted')`, now); err != nil {
		return nil, fmt.Errorf("dropping knowledge ingest feedback for unsuccessful turns: %w", err)
	}
	rows, err := db.Query(ctx, `
WITH candidates AS (
  SELECT event.id
  FROM feedback_events AS event
  JOIN conversation_turns AS turn ON turn.id = event.turn_id
  WHERE event.type = 'web_knowledge'
    AND (
      (event.status IN ('pending', 'waiting_turn')
        AND turn.status = 'completed'
        AND event.next_attempt_at_ms <= $1
        AND event.attempt_count < $2)
      OR
      (event.status = 'running'
        AND event.lease_expires_at_ms <= $1
        AND event.attempt_count < $2)
    )
  ORDER BY event.updated_at_ms, event.id
  LIMIT $3
  FOR UPDATE OF event SKIP LOCKED
)
UPDATE feedback_events AS event
SET status = 'running',
    lease_owner = $4,
    lease_expires_at_ms = $5,
    claim_group_id = event.id,
    attempt_count = event.attempt_count + 1,
    next_attempt_at_ms = 0,
    updated_at_ms = $1
FROM candidates
WHERE event.id = candidates.id
RETURNING event.id, event.conversation_id, event.turn_id,
          event.payload_json->>'taskId', event.payload_json->'source',
          event.attempt_count, event.next_attempt_at_ms,
          COALESCE(event.error_category, '')`,
		now, MaxKnowledgeIngestAttempts, limit, workerID, leaseExpires)
	if err != nil {
		return nil, fmt.Errorf("claiming knowledge ingest feedback: %w", err)
	}
	defer rows.Close()
	jobs := make([]KnowledgeIngestJob, 0, limit)
	for rows.Next() {
		var job KnowledgeIngestJob
		if err := rows.Scan(
			&job.ID, &job.ConversationID, &job.TurnID,
			&job.TaskID, &job.SourceJSON,
			&job.AttemptCount, &job.NextAttemptAt, &job.ErrorCategory,
		); err != nil {
			return nil, fmt.Errorf("scanning knowledge ingest feedback: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge ingest feedback: %w", err)
	}
	return jobs, nil
}

func RenewKnowledgeIngestJobLease(
	ctx context.Context,
	exec interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	},
	id, workerID string,
	leaseExpires, now int64,
) error {
	changed, err := exec.Exec(ctx, `
UPDATE feedback_events
SET lease_expires_at_ms = $3, updated_at_ms = $4
WHERE id = $1 AND type = 'web_knowledge'
  AND status = 'running' AND lease_owner = $2 AND claim_group_id = id`,
		id, workerID, leaseExpires, now,
	)
	if err != nil {
		return fmt.Errorf("renewing knowledge ingest feedback lease: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest feedback is not owned by this worker")
	}
	return nil
}

func ReleaseKnowledgeIngestJob(
	ctx context.Context,
	exec interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	},
	id, workerID string,
	now int64,
) error {
	changed, err := exec.Exec(ctx, `
UPDATE feedback_events
SET status = 'pending',
    attempt_count = GREATEST(0, attempt_count - 1),
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = 0,
    updated_at_ms = $3
WHERE id = $1 AND type = 'web_knowledge'
  AND status = 'running' AND lease_owner = $2 AND claim_group_id = id`,
		id, workerID, now,
	)
	if err != nil {
		return fmt.Errorf("releasing knowledge ingest feedback: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest feedback is not owned by this worker")
	}
	return nil
}

func FinishKnowledgeIngestJob(ctx context.Context, exec interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id, workerID, status, message string, now int64) error {
	changed, err := exec.Exec(ctx, `
UPDATE feedback_events
SET status = $3,
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = 0,
    error_category = CASE
      WHEN $3 = 'succeeded' THEN NULL
      WHEN error_category IS NULL AND $4 <> '' THEN 'terminal'
      ELSE error_category
    END,
    error_message = NULLIF($4, ''),
    updated_at_ms = $5
WHERE id = $1 AND type = 'web_knowledge'
  AND status = 'running' AND lease_owner = $2 AND claim_group_id = id`,
		id, workerID, status, message, now)
	if err != nil {
		return fmt.Errorf("finishing knowledge ingest feedback: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest feedback is not owned by this worker")
	}
	return nil
}
