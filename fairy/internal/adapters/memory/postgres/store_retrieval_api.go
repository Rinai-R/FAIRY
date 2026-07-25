package postgres

import (
	"context"
	"fairy/memory/semantic"
	"strings"
)

func (s *Store) Retrieve(characterID string, query string) (RetrievalContext, error) {
	return s.RetrieveContext(context.Background(), characterID, query)
}

func (s *Store) RetrieveContext(ctx context.Context, characterID string, query string) (RetrievalContext, error) {
	return s.retrievePostgres(ctx, characterID, query)
}

func (s *Store) RetrievePublicKnowledgeContext(ctx context.Context, query string) (RetrievalContext, error) {
	if s == nil || s.pool == nil {
		return RetrievalContext{}, ErrDatabasePoolEmpty
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return RetrievalContext{}, err
	}
	result := RetrievalContext{
		PersonalMemories: []RetrievedPersonalMemory{},
		Knowledge:        []RetrievedKnowledge{},
		SemanticStatus:   string(semantic.StatusUnavailable),
	}
	if normalized == "" {
		return result, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	remaining := maxRetrievedContextChars
	result.Knowledge, err = retrieveKnowledgeTrigramPostgres(queryCtx, s.pool.Raw(), normalized, &remaining)
	if err != nil {
		return RetrievalContext{}, err
	}
	return result, nil
}

func semanticQueryText(query string) string {
	trimmed := strings.TrimSpace(query)
	if len([]rune(trimmed)) < 2 {
		return ""
	}
	return trimmed
}
