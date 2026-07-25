package postgres

import (
	"context"
	"fmt"
)

var errEmbeddingJobStaleCompletion = ErrEmbeddingJobStaleCompletion

func (s *Store) claimEmbeddingJobsPostgres(ctx context.Context, limit int, now int64) ([]embeddingJob, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	leaseExpires := now + s.jobLeaseDuration.Milliseconds()
	claimed, err := ClaimEmbeddingJobs(queryCtx, s.pool.Raw(), SemanticEmbeddingModelID, SemanticEmbeddingDimensions, now, limit, s.workerID, leaseExpires)
	if err != nil {
		return nil, err
	}
	jobs := make([]embeddingJob, 0, len(claimed))
	for _, job := range claimed {
		jobs = append(jobs, embeddingJobFromAdapter(job))
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
	payload, err := LoadEmbeddingJobPayload(queryCtx, s.pool.Raw(), adapterEmbeddingJob(job), embeddingItemKindPersonalMemory, embeddingItemKindKnowledge)
	if err != nil {
		return embeddingJobPayload{}, err
	}
	return embeddingJobPayload(payload), nil
}

func (s *Store) finishEmbeddingJobSucceededPostgres(ctx context.Context, job embeddingJob, now int64) error {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning embedding job success transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := FinishEmbeddingJobSucceeded(queryCtx, tx, adapterEmbeddingJob(job), s.workerID, now); err != nil {
		return err
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
	if err := FinishEmbeddingJobFailed(queryCtx, tx, adapterEmbeddingJob(job), s.workerID, code, message, retryable, now); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing embedding job failure: %w", err)
	}
	return nil
}

func adapterEmbeddingJob(job embeddingJob) EmbeddingJob {
	return EmbeddingJob{
		ID:          job.ID,
		ItemKind:    job.ItemKind,
		ItemID:      job.ItemID,
		ModelID:     job.ModelID,
		Dimensions:  job.Dimensions,
		PointID:     job.PointID,
		ContentHash: job.ContentHash,
	}
}

func embeddingJobFromAdapter(job EmbeddingJob) embeddingJob {
	return embeddingJob(job)
}
