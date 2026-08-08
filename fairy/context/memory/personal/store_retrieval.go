package personal

import (
	"context"

	memoryretrieval "fairy/context/memory/retrieval"
)

func (s *Store) retrievePostgres(ctx context.Context, characterID, query string) (Retrieval, error) {
	return s.retrievePostgresHybrid(ctx, characterID, query)
}

func normalizePostgresSearchQuery(query string) (string, error) {
	return memoryretrieval.NormalizePostgresQuery(query)
}
