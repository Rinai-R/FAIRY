package postgres

import (
	"context"
	"errors"
	"fmt"

	domainmemory "fairy/internal/domain/memory"

	"github.com/jackc/pgx/v5"
)

func ReadVectorMetrics(ctx context.Context, db RowQuerier) (domainmemory.VectorMetrics, error) {
	var metrics domainmemory.VectorMetrics
	err := db.QueryRow(ctx, `
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
		return domainmemory.VectorMetrics{}, fmt.Errorf("reading embedding job metrics: %w", err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM memory_embedding_items WHERE status = 'embedded'").Scan(&metrics.EmbeddingJobs.Embedded); err != nil {
		return domainmemory.VectorMetrics{}, fmt.Errorf("reading embedded item metrics: %w", err)
	}
	err = db.QueryRow(ctx, `
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
			return domainmemory.VectorMetrics{}, fmt.Errorf("reading reconciliation metrics: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return metrics, nil
		}
		return domainmemory.VectorMetrics{}, fmt.Errorf("reading reconciliation metrics: %w", err)
	}
	metrics.Reconciliation.Observed = true
	return metrics, nil
}
