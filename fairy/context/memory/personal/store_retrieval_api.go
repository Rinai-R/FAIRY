package personal

import (
	"context"
	"strings"
)

func (s *Store) Retrieve(characterID string, query string) (Retrieval, error) {
	return s.RetrieveContext(context.Background(), characterID, query)
}

func (s *Store) RetrieveContext(ctx context.Context, characterID string, query string) (Retrieval, error) {
	if !s.usesSeekDB() {
		return Retrieval{}, ErrStoreBackendUnavailable
	}
	return s.retrieveSeekDB(ctx, characterID, query)
}

func semanticQueryText(query string) string {
	trimmed := strings.TrimSpace(query)
	if len([]rune(trimmed)) < 2 {
		return ""
	}
	return trimmed
}
