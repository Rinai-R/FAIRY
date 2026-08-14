package ledger

import "context"

func (s *Store) AggregateTokenUsage(limit int) (UsageReport, error) {
	return s.AggregateTokenUsageContext(context.Background(), limit)
}

func (s *Store) AggregateTokenUsageContext(ctx context.Context, limit int) (UsageReport, error) {
	if s.usesSeekDB() {
		return s.aggregateTokenUsageSeekDB(ctx, limit)
	}
	if !s.usesPostgres() {
		return UsageReport{}, ErrStoreBackendUnavailable
	}
	return s.aggregateTokenUsagePostgres(ctx, limit)
}
