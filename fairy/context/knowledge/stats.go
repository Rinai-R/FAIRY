package knowledge

import (
	"context"
	"fmt"
)

type Stats struct {
	Candidates int64 `json:"candidates"`
	Verified   int64 `json:"verified"`
	VectorRows int64 `json:"vectorRows"`
}

func (s *Store) StatsContext(ctx context.Context) (Stats, error) {
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return Stats{}, ErrDatabasePoolEmpty
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var stats Stats
	if err := s.pool.Raw().QueryRow(queryCtx, `
SELECT
  count(*) FILTER (WHERE status = 'candidate'),
  count(*) FILTER (WHERE status = 'verified'),
  count(*) FILTER (WHERE embedding_v2 IS NOT NULL)
FROM knowledge_entries`).Scan(&stats.Candidates, &stats.Verified, &stats.VectorRows); err != nil {
		return Stats{}, fmt.Errorf("reading knowledge stats: %w", err)
	}
	return stats, nil
}
