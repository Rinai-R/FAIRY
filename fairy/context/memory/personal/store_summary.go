package personal

import "context"

func (s *Store) Summary() (Summary, error) {
	return s.SummaryContext(context.Background())
}

func (s *Store) SummaryContext(ctx context.Context) (Summary, error) {
	if !s.usesSeekDB() {
		return Summary{}, ErrStoreBackendUnavailable
	}
	return s.summarySeekDB(ctx)
}
