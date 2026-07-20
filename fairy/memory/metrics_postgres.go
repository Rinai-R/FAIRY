package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type EmbeddingJobMetrics struct {
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
	Embedded  int64 `json:"embeddedItems"`
}

type ReconciliationMetrics struct {
	Observed      bool  `json:"observed"`
	MissingPoints int64 `json:"missingPoints"`
	StalePoints   int64 `json:"stalePoints"`
	OrphanPoints  int64 `json:"orphanPoints"`
}

type VectorMetrics struct {
	EmbeddingJobs  EmbeddingJobMetrics   `json:"embeddingJobs"`
	Reconciliation ReconciliationMetrics `json:"reconciliation"`
}

func (s *Store) VectorMetricsContext(ctx context.Context) (VectorMetrics, error) {
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return VectorMetrics{}, ErrDatabasePoolEmpty
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()

	var metrics VectorMetrics
	err := s.pool.Raw().QueryRow(queryCtx, `
SELECT
  count(*) FILTER (WHERE status = 'pending'),
  count(*) FILTER (WHERE status = 'running'),
  count(*) FILTER (WHERE status = 'succeeded'),
  count(*) FILTER (WHERE status = 'failed')
FROM memory_embedding_jobs`).Scan(
		&metrics.EmbeddingJobs.Pending,
		&metrics.EmbeddingJobs.Running,
		&metrics.EmbeddingJobs.Succeeded,
		&metrics.EmbeddingJobs.Failed,
	)
	if err != nil {
		return VectorMetrics{}, fmt.Errorf("reading embedding job metrics: %w", err)
	}
	if err := s.pool.Raw().QueryRow(queryCtx, "SELECT count(*) FROM memory_embedding_items WHERE status = 'embedded'").Scan(&metrics.EmbeddingJobs.Embedded); err != nil {
		return VectorMetrics{}, fmt.Errorf("reading embedded item metrics: %w", err)
	}
	err = s.pool.Raw().QueryRow(queryCtx, `
SELECT missing_points, stale_points, orphan_points
FROM vector_reconciliation_runs
WHERE status = 'succeeded'
ORDER BY updated_at_ms DESC, id ASC
LIMIT 1`).Scan(
		&metrics.Reconciliation.MissingPoints,
		&metrics.Reconciliation.StalePoints,
		&metrics.Reconciliation.OrphanPoints,
	)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return VectorMetrics{}, fmt.Errorf("reading reconciliation metrics: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return metrics, nil
		}
		return VectorMetrics{}, fmt.Errorf("reading reconciliation metrics: %w", err)
	}
	metrics.Reconciliation.Observed = true
	return metrics, nil
}
