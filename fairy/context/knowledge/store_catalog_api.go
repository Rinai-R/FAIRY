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

func (s *Store) Catalog() (Catalog, error) {
	return s.CatalogContext(context.Background())
}

func (s *Store) CatalogContext(ctx context.Context) (Catalog, error) {
	if !s.usesSeekDB() {
		return Catalog{}, ErrStoreBackendUnavailable
	}
	return s.catalogSeekDB(ctx)
}

func (s *Store) ConfirmCandidate(id string) (Record, error) {
	return s.confirmCandidate(context.Background(), id, false)
}

func (s *Store) ConfirmCandidateContext(ctx context.Context, id string) (Record, error) {
	return s.confirmCandidate(ctx, id, true)
}

func (s *Store) confirmCandidate(ctx context.Context, id string, requireContext bool) (Record, error) {
	if !s.usesSeekDB() {
		return Record{}, ErrStoreBackendUnavailable
	}
	return s.confirmCandidateSeekDB(ctx, id, requireContext)
}

func (s *Store) Tombstone(id string) error {
	return s.TombstoneContext(context.Background(), id)
}

func (s *Store) TombstoneContext(ctx context.Context, id string) error {
	if !s.usesSeekDB() {
		return ErrStoreBackendUnavailable
	}
	return s.tombstoneSeekDB(ctx, id)
}

func (s *Store) StatsContext(ctx context.Context) (Stats, error) {
	if !s.usesSeekDB() {
		return Stats{}, ErrStoreBackendUnavailable
	}
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
