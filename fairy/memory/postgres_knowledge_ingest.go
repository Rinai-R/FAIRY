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
	BatchID        string
	SourcesJSON    []byte
	Query          string
	Title          string
	URL            string
	Snippet        string
	Rank           uint8
	FetchedAt      int64
	AttemptCount   int
	NextAttemptAt  int64
	ErrorCategory  string
}

func EnqueueKnowledgeIngestBatch(
	ctx context.Context,
	tx pgx.Tx,
	id, conversationID, turnID, batchID string,
	sourcesJSON []byte,
	now int64,
) error {
	_, err := tx.Exec(ctx, `
INSERT INTO knowledge_ingest_jobs(
  id, conversation_id, turn_id, batch_id, sources_json, query,
  title, url, snippet, rank, fetched_at_ms,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5::jsonb, '', '', '', '', 0, 0, 'waiting_turn', $6, $6)
ON CONFLICT (batch_id) WHERE batch_id <> '' DO NOTHING`,
		id, conversationID, turnID, batchID, sourcesJSON, now,
	)
	if err != nil {
		return fmt.Errorf("queueing knowledge ingest batch: %w", err)
	}
	return nil
}

func EnqueueKnowledgeIngestJob(
	ctx context.Context,
	tx pgx.Tx,
	id, conversationID, turnID, query, title, url, snippet string,
	rank uint8,
	fetchedAt, now int64,
) error {
	_, err := tx.Exec(ctx, `
INSERT INTO knowledge_ingest_jobs(
  id, conversation_id, turn_id, query, title, url, snippet, rank, fetched_at_ms,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending', $10, $10)`, id, conversationID, turnID, query, title, url, snippet, rank, fetchedAt, now)
	if err != nil {
		return fmt.Errorf("queueing knowledge ingest job: %w", err)
	}
	return nil
}

type knowledgeIngestQuerier interface {
	Querier
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func ClaimKnowledgeIngestJobs(ctx context.Context, db knowledgeIngestQuerier, limit int, now int64, workerID string, leaseExpires int64) ([]KnowledgeIngestJob, error) {
	return claimKnowledgeIngestJobs(ctx, db, limit, now, workerID, leaseExpires, false)
}

func ClaimLegacyKnowledgeIngestJobs(ctx context.Context, db knowledgeIngestQuerier, limit int, now int64, workerID string, leaseExpires int64) ([]KnowledgeIngestJob, error) {
	return claimKnowledgeIngestJobs(ctx, db, limit, now, workerID, leaseExpires, true)
}

func claimKnowledgeIngestJobs(ctx context.Context, db knowledgeIngestQuerier, limit int, now int64, workerID string, leaseExpires int64, legacyOnly bool) ([]KnowledgeIngestJob, error) {
	legacyFilter := ""
	if legacyOnly {
		legacyFilter = " AND batch_id = ''"
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
  )`+legacyFilter+`
  ORDER BY j.updated_at_ms ASC, j.id ASC
  LIMIT $2
  FOR UPDATE OF j SKIP LOCKED
)
UPDATE knowledge_ingest_jobs j
SET status = 'running', lease_owner = $3, lease_expires_at_ms = $4,
    attempt_count = j.attempt_count + 1, updated_at_ms = $1
FROM candidates c
WHERE j.id = c.id
RETURNING j.id, j.conversation_id, j.turn_id, j.query, j.title, j.url, j.snippet,
          j.rank, j.fetched_at_ms, j.batch_id, j.sources_json,
          j.attempt_count, j.next_attempt_at_ms, COALESCE(j.error_category, '')`, now, limit, workerID, leaseExpires)
	if err != nil {
		return nil, fmt.Errorf("claiming knowledge ingest jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]KnowledgeIngestJob, 0, limit)
	for rows.Next() {
		var job KnowledgeIngestJob
		var rank int
		if err := rows.Scan(
			&job.ID, &job.ConversationID, &job.TurnID, &job.Query,
			&job.Title, &job.URL, &job.Snippet, &rank, &job.FetchedAt,
			&job.BatchID, &job.SourcesJSON, &job.AttemptCount, &job.NextAttemptAt, &job.ErrorCategory,
		); err != nil {
			return nil, fmt.Errorf("scanning claimed knowledge ingest job: %w", err)
		}
		if job.BatchID == "" && (rank < 1 || rank > 5) {
			return nil, errors.New("claimed knowledge ingest rank is invalid")
		}
		if rank >= 0 && rank <= 255 {
			job.Rank = uint8(rank)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating claimed knowledge ingest jobs: %w", err)
	}
	return jobs, nil
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
