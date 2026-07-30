package memory

import "context"

func (s *Store) LoadCommittedMemoryCoverage(conversationID string) ([]MemoryContextCoverage, error) {
	return s.LoadCommittedMemoryCoverageContext(context.Background(), conversationID)
}

func (s *Store) LoadCommittedMemoryCoverageContext(ctx context.Context, conversationID string) ([]MemoryContextCoverage, error) {
	return s.loadCommittedMemoryCoveragePostgres(ctx, conversationID)
}
