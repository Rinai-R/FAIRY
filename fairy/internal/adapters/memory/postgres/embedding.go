package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const MaxEmbeddingJobsPerPass = 100

var (
	ErrEmbeddingJobStaleCompletion = errors.New("embedding job completion is stale or not owned by this worker")
	ErrEmbeddingJobStaleItem       = errors.New("embedding job item is no longer current")
)

type EmbeddingJob struct {
	ID          string
	ItemKind    string
	ItemID      string
	ModelID     string
	Dimensions  int
	PointID     string
	ContentHash string
}

type EmbeddingJobPayload struct {
	Content     string
	ScopeType   string
	CharacterID string
}

func EnqueueEmbeddingJob(
	ctx context.Context,
	tx pgx.Tx,
	jobID, itemKind, itemID, modelID string,
	dimensions int,
	pointID, contentHash string,
	now int64,
) error {
	_, err := tx.Exec(ctx, `
INSERT INTO memory_embedding_items(
  id, item_kind, item_id, model_id, dimensions, point_id, content_hash,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $8)
ON CONFLICT(item_kind, item_id, model_id) DO UPDATE SET
  dimensions = excluded.dimensions,
  point_id = excluded.point_id,
  content_hash = excluded.content_hash,
  status = 'pending',
  error_code = NULL,
  error_message = NULL,
  embedded_at_ms = NULL,
  updated_at_ms = excluded.updated_at_ms
WHERE memory_embedding_items.content_hash IS DISTINCT FROM excluded.content_hash`,
		jobID, itemKind, itemID, modelID, dimensions, pointID, contentHash, now)
	if err != nil {
		return fmt.Errorf("upserting semantic embedding item: %w", err)
	}
	_, err = tx.Exec(ctx, `
INSERT INTO memory_embedding_jobs(
  id, item_kind, item_id, model_id, dimensions, point_id, content_hash,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $8)
ON CONFLICT(item_kind, item_id, model_id, content_hash) DO NOTHING`,
		jobID, itemKind, itemID, modelID, dimensions, pointID, contentHash, now)
	if err != nil {
		return fmt.Errorf("queueing semantic embedding job: %w", err)
	}
	return nil
}

func ClaimEmbeddingJobs(
	ctx context.Context,
	db Querier,
	modelID string,
	dimensions int,
	now int64,
	limit int,
	workerID string,
	leaseExpires int64,
) ([]EmbeddingJob, error) {
	if limit < 1 || limit > MaxEmbeddingJobsPerPass {
		return nil, fmt.Errorf("embedding job limit must be between 1 and %d", MaxEmbeddingJobsPerPass)
	}
	rows, err := db.Query(ctx, `
WITH candidates AS (
  SELECT id FROM memory_embedding_jobs
  WHERE model_id = $1 AND dimensions = $2
    AND (status = 'pending' OR (status = 'running' AND lease_expires_at_ms <= $3))
  ORDER BY updated_at_ms ASC, id ASC
  LIMIT $4
  FOR UPDATE SKIP LOCKED
)
UPDATE memory_embedding_jobs j
SET status = 'running', lease_owner = $5, lease_expires_at_ms = $6,
    attempt_count = j.attempt_count + 1, updated_at_ms = $3
FROM candidates c
WHERE j.id = c.id
RETURNING j.id, j.item_kind, j.item_id, j.model_id, j.dimensions,
          j.point_id::text, j.content_hash`, modelID, dimensions, now, limit, workerID, leaseExpires)
	if err != nil {
		return nil, fmt.Errorf("claiming embedding jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]EmbeddingJob, 0, limit)
	for rows.Next() {
		var job EmbeddingJob
		if err := rows.Scan(&job.ID, &job.ItemKind, &job.ItemID, &job.ModelID, &job.Dimensions, &job.PointID, &job.ContentHash); err != nil {
			return nil, fmt.Errorf("scanning claimed embedding job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating claimed embedding jobs: %w", err)
	}
	return jobs, nil
}

func LoadEmbeddingJobPayload(ctx context.Context, db RowQuerier, job EmbeddingJob, personalMemoryKind, knowledgeKind string) (EmbeddingJobPayload, error) {
	var payload EmbeddingJobPayload
	var err error
	switch job.ItemKind {
	case personalMemoryKind:
		err = db.QueryRow(ctx, "SELECT content, scope_kind, COALESCE(character_id, '') FROM personal_memories WHERE id = $1 AND review_status = 'ready' AND status = 'active'", job.ItemID).Scan(&payload.Content, &payload.ScopeType, &payload.CharacterID)
	case knowledgeKind:
		err = db.QueryRow(ctx, "SELECT topic || chr(10) || statement, 'knowledge', '' FROM knowledge_entries WHERE id = $1 AND status = 'verified'", job.ItemID).Scan(&payload.Content, &payload.ScopeType, &payload.CharacterID)
	default:
		return EmbeddingJobPayload{}, fmt.Errorf("%w: unsupported item kind %q", ErrEmbeddingJobStaleItem, job.ItemKind)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return EmbeddingJobPayload{}, ErrEmbeddingJobStaleItem
	}
	if err != nil {
		return EmbeddingJobPayload{}, fmt.Errorf("reading embedding job content: %w", err)
	}
	return payload, nil
}

func FinishEmbeddingJobSucceeded(ctx context.Context, tx pgx.Tx, job EmbeddingJob, workerID string, now int64) error {
	item, err := tx.Exec(ctx, `
UPDATE memory_embedding_items
SET status = 'embedded', error_code = NULL, error_message = NULL,
    embedded_at_ms = $6, updated_at_ms = $6
WHERE item_kind = $1 AND item_id = $2 AND model_id = $3
  AND point_id = $4::uuid AND content_hash = $5`, job.ItemKind, job.ItemID, job.ModelID, job.PointID, job.ContentHash, now)
	if err != nil {
		return fmt.Errorf("marking embedding item embedded: %w", err)
	}
	if item.RowsAffected() != 1 {
		return ErrEmbeddingJobStaleCompletion
	}
	completed, err := tx.Exec(ctx, `
UPDATE memory_embedding_jobs
SET status = 'succeeded', lease_owner = NULL, lease_expires_at_ms = NULL,
    error_code = NULL, error_message = NULL, retryable = false, updated_at_ms = $4
WHERE id = $1 AND status = 'running' AND lease_owner = $2 AND content_hash = $3`, job.ID, workerID, job.ContentHash, now)
	if err != nil {
		return fmt.Errorf("marking embedding job succeeded: %w", err)
	}
	if completed.RowsAffected() != 1 {
		return ErrEmbeddingJobStaleCompletion
	}
	return nil
}

func FinishEmbeddingJobFailed(ctx context.Context, tx pgx.Tx, job EmbeddingJob, workerID, code, message string, retryable bool, now int64) error {
	message = CleanEmbeddingErrorMessage(message)
	item, err := tx.Exec(ctx, `
UPDATE memory_embedding_items
SET status = 'failed', error_code = $6, error_message = $7,
    embedded_at_ms = NULL, updated_at_ms = $8
WHERE item_kind = $1 AND item_id = $2 AND model_id = $3
  AND point_id = $4::uuid AND content_hash = $5`, job.ItemKind, job.ItemID, job.ModelID, job.PointID, job.ContentHash, code, message, now)
	if err != nil {
		return fmt.Errorf("marking embedding item failed: %w", err)
	}
	if item.RowsAffected() != 1 {
		return ErrEmbeddingJobStaleCompletion
	}
	failed, err := tx.Exec(ctx, `
UPDATE memory_embedding_jobs
SET status = 'failed', lease_owner = NULL, lease_expires_at_ms = NULL,
    error_code = $4, error_message = $5, retryable = $6, updated_at_ms = $7
WHERE id = $1 AND status = 'running' AND lease_owner = $2 AND content_hash = $3`, job.ID, workerID, job.ContentHash, code, message, retryable, now)
	if err != nil {
		return fmt.Errorf("marking embedding job failed: %w", err)
	}
	if failed.RowsAffected() != 1 {
		return ErrEmbeddingJobStaleCompletion
	}
	return nil
}

func CleanEmbeddingErrorMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "embedding job failed"
	}
	message = strings.Join(strings.Fields(message), " ")
	const maxErrorMessageLength = 500
	if len(message) > maxErrorMessageLength {
		return message[:maxErrorMessageLength]
	}
	return message
}
