package memory

import (
	"context"
)

func (s *Store) retrievePostgres(ctx context.Context, characterID, query string) (RetrievalContext, error) {
	return s.retrievePostgresHybrid(ctx, characterID, query)
}

func normalizePostgresSearchQuery(query string) (string, error) {
	return NormalizePostgresSearchQuery(query)
}

func retrieveKnowledgeTrigramPostgres(ctx context.Context, db Querier, query string, remaining *int) ([]RetrievedKnowledge, error) {
	return RetrieveKnowledgeTrigram(ctx, db, query, remaining)
}
