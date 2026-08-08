package extraction

import "context"

func (s *Store) LoadCommittedMemoryCoverage(conversationID string) ([]Coverage, error) {
	return s.LoadCommittedMemoryCoverageContext(context.Background(), conversationID)
}

func (s *Store) LoadCommittedMemoryCoverageContext(ctx context.Context, conversationID string) ([]Coverage, error) {
	return s.loadCommittedMemoryCoveragePostgres(ctx, conversationID)
}
