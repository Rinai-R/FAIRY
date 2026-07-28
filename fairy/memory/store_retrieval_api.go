package memory

import (
	"context"
	"strings"
)

func (s *Store) Retrieve(characterID string, query string) (RetrievalContext, error) {
	return s.RetrieveContext(context.Background(), characterID, query)
}

func (s *Store) RetrieveContext(ctx context.Context, characterID string, query string) (RetrievalContext, error) {
	return s.retrievePostgres(ctx, characterID, query)
}

func (s *Store) RetrievePublicKnowledgeContext(ctx context.Context, query string) (RetrievalContext, error) {
	return s.retrievePublicKnowledgePostgres(ctx, query)
}

func semanticQueryText(query string) string {
	trimmed := strings.TrimSpace(query)
	if len([]rune(trimmed)) < 2 {
		return ""
	}
	return trimmed
}
