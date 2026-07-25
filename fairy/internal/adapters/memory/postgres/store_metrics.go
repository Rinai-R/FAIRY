package postgres

import (
	"context"
	domainmemory "fairy/internal/domain/memory"
)

type EmbeddingJobMetrics = domainmemory.EmbeddingJobMetrics
type ReconciliationMetrics = domainmemory.ReconciliationMetrics

func (s *Store) VectorMetricsContext(ctx context.Context) (VectorMetrics, error) {
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return VectorMetrics{}, ErrDatabasePoolEmpty
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return ReadVectorMetrics(queryCtx, s.pool.Raw())
}
