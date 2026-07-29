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
	MaxKnowledgeIngestAttempts    = 3
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

func EnqueueKnowledgeIngestTask(
	ctx context.Context,
	tx pgx.Tx,
	id, conversationID, turnID, taskID string,
	sourceJSON []byte,
	now int64,
) error {
	_, err := tx.Exec(ctx, `
INSERT INTO knowledge_ingest_jobs(
  id, conversation_id, turn_id, task_id, source_json,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5::jsonb, 'waiting_turn', $6, $6)
ON CONFLICT (task_id) WHERE task_id <> '' DO NOTHING`,
		id, conversationID, turnID, taskID, sourceJSON, now,
	)
	if err != nil {
		return fmt.Errorf("queueing knowledge ingest task: %w", err)
	}
	return nil
}

type knowledgeIngestQuerier interface {
	Querier
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func ClaimKnowledgeIngestJobs(ctx context.Context, db knowledgeIngestQuerier, limit int, now int64, workerID string, leaseExpires int64) ([]KnowledgeIngestJob, error) {
	if _, err := db.Exec(ctx, `
UPDATE knowledge_ingest_jobs
SET status = 'failed',
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    next_attempt_at_ms = 0,
    error_category = 'attempts_exhausted',
    error_message = 'knowledge ingest lease expired after maximum attempts',
    updated_at_ms = $1
WHERE status = 'running'
  AND lease_expires_at_ms <= $1
  AND attempt_count >= `+fmt.Sprint(MaxKnowledgeIngestAttempts)+`
  AND task_id <> ''`, now); err != nil {
		return nil, fmt.Errorf("failing exhausted knowledge ingest jobs: %w", err)
	}
	if _, err := db.Exec(ctx, `
UPDATE knowledge_ingest_jobs j
SET status = 'dropped', error_message = 'source turn did not complete', updated_at_ms = $1
FROM conversation_turns t
WHERE t.id = j.turn_id
  AND j.status = 'waiting_turn'
  AND t.status IN ('failed', 'interrupted')`, now); err != nil {
		return nil, fmt.Errorf("dropping knowledge ingest jobs for unsuccessful turns: %w", err)
	}
	rows, err := db.Query(ctx, `
WITH candidates AS (
  SELECT j.id FROM knowledge_ingest_jobs j
  JOIN conversation_turns t ON t.id = j.turn_id
  WHERE (
    (j.status = 'pending' AND t.status = 'completed' AND j.next_attempt_at_ms <= $1 AND j.attempt_count < `+fmt.Sprint(MaxKnowledgeIngestAttempts)+`)
    OR (j.status = 'waiting_turn' AND t.status = 'completed')
    OR (j.status = 'running' AND j.lease_expires_at_ms <= $1 AND j.attempt_count < `+fmt.Sprint(MaxKnowledgeIngestAttempts)+`)
  )
    AND j.task_id <> ''
  ORDER BY j.updated_at_ms ASC, j.id ASC
  LIMIT $2
  FOR UPDATE OF j SKIP LOCKED
)
UPDATE knowledge_ingest_jobs j
SET status = 'running', lease_owner = $3, lease_expires_at_ms = $4,
    attempt_count = j.attempt_count + 1, updated_at_ms = $1
FROM candidates c
WHERE j.id = c.id
RETURNING j.id, j.conversation_id, j.turn_id, j.task_id, j.source_json,
          j.attempt_count, j.next_attempt_at_ms, COALESCE(j.error_category, '')`, now, limit, workerID, leaseExpires)
	if err != nil {
		return nil, fmt.Errorf("claiming knowledge ingest jobs: %w", err)
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
			return nil, fmt.Errorf("scanning claimed knowledge ingest job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating claimed knowledge ingest jobs: %w", err)
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
UPDATE knowledge_ingest_jobs
SET lease_expires_at_ms = $3, updated_at_ms = $4
WHERE id = $1 AND status = 'running' AND lease_owner = $2`,
		id, workerID, leaseExpires, now,
	)
	if err != nil {
		return fmt.Errorf("renewing knowledge ingest job lease: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest job is not owned by this worker")
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
UPDATE knowledge_ingest_jobs
SET status = 'pending',
    attempt_count = GREATEST(0, attempt_count - 1),
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    next_attempt_at_ms = 0,
    updated_at_ms = $3
WHERE id = $1 AND status = 'running' AND lease_owner = $2`,
		id, workerID, now,
	)
	if err != nil {
		return fmt.Errorf("releasing knowledge ingest job: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest job is not owned by this worker")
	}
	return nil
}

func FinishKnowledgeIngestJob(ctx context.Context, exec interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id, workerID, status, message string, now int64) error {
	changed, err := exec.Exec(ctx, `
UPDATE knowledge_ingest_jobs
SET status = $3, lease_owner = NULL, lease_expires_at_ms = NULL,
    next_attempt_at_ms = 0,
    error_category = CASE
      WHEN $3 = 'succeeded' THEN NULL
      WHEN error_category IS NULL AND $4 <> '' THEN 'terminal'
      ELSE error_category
    END,
    error_message = NULLIF($4, ''), updated_at_ms = $5
WHERE id = $1 AND status = 'running' AND lease_owner = $2`, id, workerID, status, message, now)
	if err != nil {
		return fmt.Errorf("finishing knowledge ingest job: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest job is not owned by this worker")
	}
	return nil
}
