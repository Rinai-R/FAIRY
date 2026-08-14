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
	if s.usesSeekDB() {
		queryCtx, cancel := s.seekDBQueryContext(ctx)
		defer cancel()
		var stats Stats
		if err := s.seekDB.QueryRowContext(queryCtx, `
SELECT
  COALESCE(SUM(CASE WHEN status = 'candidate' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'verified' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN embedding IS NOT NULL THEN 1 ELSE 0 END), 0)
FROM knowledge_entries`).Scan(&stats.Candidates, &stats.Verified, &stats.VectorRows); err != nil {
			return Stats{}, fmt.Errorf("reading SeekDB knowledge stats: %w", err)
		}
		return stats, nil
	}
	if !s.usesPostgres() {
		return Stats{}, ErrStoreBackendUnavailable
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
