package knowledge

import "context"

func (s *Store) RetrieveContext(ctx context.Context, query string) (Retrieval, error) {
	if !s.usesSeekDB() {
		return Retrieval{}, ErrStoreBackendUnavailable
	}
	return s.retrieveSeekDB(ctx, query)
}
