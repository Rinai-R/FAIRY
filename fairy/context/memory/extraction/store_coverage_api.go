package extraction

import "context"

func (s *Store) LoadCommittedMemoryCoverage(conversationID string) ([]Coverage, error) {
	return s.LoadCommittedMemoryCoverageContext(context.Background(), conversationID)
}

func (s *Store) LoadCommittedMemoryCoverageContext(ctx context.Context, conversationID string) ([]Coverage, error) {
	if s.usesSeekDB() {
		return s.loadCommittedMemoryCoverageSeekDB(ctx, conversationID)
	}
	if !s.usesPostgres() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.loadCommittedMemoryCoveragePostgres(ctx, conversationID)
}
