package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const maxEmbeddingJobsPerPass = 100

var errEmbeddingJobStaleCompletion = errors.New("embedding job completion is stale or not owned by this worker")

func (s *Store) claimEmbeddingJobsPostgres(ctx context.Context, limit int, now int64) ([]embeddingJob, error) {
	if limit < 1 || limit > maxEmbeddingJobsPerPass {
		return nil, fmt.Errorf("embedding job limit must be between 1 and %d", maxEmbeddingJobsPerPass)
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	leaseExpires := now + s.jobLeaseDuration.Milliseconds()
	rows, err := s.pool.Raw().Query(queryCtx, `
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
          j.point_id::text, j.content_hash`, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, now, limit, s.workerID, leaseExpires)
	if err != nil {
		return nil, fmt.Errorf("claiming embedding jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]embeddingJob, 0, limit)
	for rows.Next() {
		var job embeddingJob
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

func (s *Store) embeddingJobContentPostgres(ctx context.Context, job embeddingJob) (string, error) {
	payload, err := s.embeddingJobPayloadPostgres(ctx, job)
	if err != nil {
		return "", err
	}
	return payload.Content, nil
}

func (s *Store) embeddingJobPayloadPostgres(ctx context.Context, job embeddingJob) (embeddingJobPayload, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var err error
	var payload embeddingJobPayload
	switch job.ItemKind {
	case embeddingItemKindPersonalMemory:
		err = s.pool.Raw().QueryRow(queryCtx, "SELECT content, scope_kind, COALESCE(character_id, '') FROM personal_memories WHERE id = $1 AND review_status = 'ready' AND status = 'active'", job.ItemID).Scan(&payload.Content, &payload.ScopeType, &payload.CharacterID)
	case embeddingItemKindKnowledge:
		err = s.pool.Raw().QueryRow(queryCtx, "SELECT topic || chr(10) || statement, 'knowledge', '' FROM knowledge_entries WHERE id = $1 AND status = 'verified'", job.ItemID).Scan(&payload.Content, &payload.ScopeType, &payload.CharacterID)
	default:
		return embeddingJobPayload{}, fmt.Errorf("%w: unsupported item kind %q", errEmbeddingJobStale, job.ItemKind)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return embeddingJobPayload{}, errEmbeddingJobStale
	}
	if err != nil {
		return embeddingJobPayload{}, fmt.Errorf("reading embedding job content: %w", err)
	}
	return payload, nil
}

func (s *Store) finishEmbeddingJobSucceededPostgres(ctx context.Context, job embeddingJob, now int64) error {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning embedding job success transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	item, err := tx.Exec(queryCtx, `
UPDATE memory_embedding_items
SET status = 'embedded', error_code = NULL, error_message = NULL,
    embedded_at_ms = $6, updated_at_ms = $6
WHERE item_kind = $1 AND item_id = $2 AND model_id = $3
  AND point_id = $4::uuid AND content_hash = $5`, job.ItemKind, job.ItemID, job.ModelID, job.PointID, job.ContentHash, now)
	if err != nil {
		return fmt.Errorf("marking embedding item embedded: %w", err)
	}
	if item.RowsAffected() != 1 {
		return errEmbeddingJobStaleCompletion
	}
	completed, err := tx.Exec(queryCtx, `
UPDATE memory_embedding_jobs
SET status = 'succeeded', lease_owner = NULL, lease_expires_at_ms = NULL,
    error_code = NULL, error_message = NULL, retryable = false, updated_at_ms = $4
WHERE id = $1 AND status = 'running' AND lease_owner = $2 AND content_hash = $3`, job.ID, s.workerID, job.ContentHash, now)
	if err != nil {
		return fmt.Errorf("marking embedding job succeeded: %w", err)
	}
	if completed.RowsAffected() != 1 {
		return errEmbeddingJobStaleCompletion
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing embedding job success: %w", err)
	}
	return nil
}

func (s *Store) finishEmbeddingJobFailedPostgres(ctx context.Context, job embeddingJob, code, message string, retryable bool, now int64) error {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning embedding job failure transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	message = cleanEmbeddingErrorMessage(message)
	item, err := tx.Exec(queryCtx, `
UPDATE memory_embedding_items
SET status = 'failed', error_code = $6, error_message = $7,
    embedded_at_ms = NULL, updated_at_ms = $8
WHERE item_kind = $1 AND item_id = $2 AND model_id = $3
  AND point_id = $4::uuid AND content_hash = $5`, job.ItemKind, job.ItemID, job.ModelID, job.PointID, job.ContentHash, code, message, now)
	if err != nil {
		return fmt.Errorf("marking embedding item failed: %w", err)
	}
	if item.RowsAffected() != 1 {
		return errEmbeddingJobStaleCompletion
	}
	failed, err := tx.Exec(queryCtx, `
UPDATE memory_embedding_jobs
SET status = 'failed', lease_owner = NULL, lease_expires_at_ms = NULL,
    error_code = $4, error_message = $5, retryable = $6, updated_at_ms = $7
WHERE id = $1 AND status = 'running' AND lease_owner = $2 AND content_hash = $3`, job.ID, s.workerID, job.ContentHash, code, message, retryable, now)
	if err != nil {
		return fmt.Errorf("marking embedding job failed: %w", err)
	}
	if failed.RowsAffected() != 1 {
		return errEmbeddingJobStaleCompletion
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing embedding job failure: %w", err)
	}
	return nil
}
