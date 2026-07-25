package postgres

import "context"

func (s *Store) AggregateTokenUsage(limit int) (UsageReport, error) {
	return s.AggregateTokenUsageContext(context.Background(), limit)
}

func (s *Store) AggregateTokenUsageContext(ctx context.Context, limit int) (UsageReport, error) {
	return s.aggregateTokenUsagePostgres(ctx, limit)
}
