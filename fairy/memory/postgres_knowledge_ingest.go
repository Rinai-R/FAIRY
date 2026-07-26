package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const MaxKnowledgeIngestJobsPerPass = 100

type KnowledgeIngestJob struct {
	ID             string
	ConversationID string
	TurnID         string
	Query          string
	Title          string
	URL            string
	Snippet        string
	Rank           uint8
	FetchedAt      int64
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

func ClaimKnowledgeIngestJobs(ctx context.Context, db Querier, limit int, now int64, workerID string, leaseExpires int64) ([]KnowledgeIngestJob, error) {
	rows, err := db.Query(ctx, `
WITH candidates AS (
  SELECT id FROM knowledge_ingest_jobs
  WHERE status = 'pending' OR (status = 'running' AND lease_expires_at_ms <= $1)
  ORDER BY updated_at_ms ASC, id ASC
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
UPDATE knowledge_ingest_jobs j
SET status = 'running', lease_owner = $3, lease_expires_at_ms = $4,
    attempt_count = j.attempt_count + 1, updated_at_ms = $1
FROM candidates c
WHERE j.id = c.id
RETURNING j.id, j.conversation_id, j.turn_id, j.query, j.title, j.url, j.snippet, j.rank, j.fetched_at_ms`, now, limit, workerID, leaseExpires)
	if err != nil {
		return nil, fmt.Errorf("claiming knowledge ingest jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]KnowledgeIngestJob, 0, limit)
	for rows.Next() {
		var job KnowledgeIngestJob
		var rank int
		if err := rows.Scan(&job.ID, &job.ConversationID, &job.TurnID, &job.Query, &job.Title, &job.URL, &job.Snippet, &rank, &job.FetchedAt); err != nil {
			return nil, fmt.Errorf("scanning claimed knowledge ingest job: %w", err)
		}
		if rank < 1 || rank > 5 {
			return nil, errors.New("claimed knowledge ingest rank is invalid")
		}
		job.Rank = uint8(rank)
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
