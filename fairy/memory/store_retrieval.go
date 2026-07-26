package memory

import (
	"context"
	"fairy/memory/semantic"
)

func (s *Store) retrievePostgres(ctx context.Context, characterID, query string) (RetrievalContext, error) {
	if err := ValidateID("character_id", characterID); err != nil {
		return RetrievalContext{}, err
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return RetrievalContext{}, err
	}
	if normalized == "" {
		return RetrievalContext{SemanticStatus: string(semantic.StatusUnavailable)}, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	remaining := maxRetrievedContextChars
	memories, err := retrievePersonalTrigramPostgres(queryCtx, s.pool.Raw(), characterID, normalized, &remaining)
	if err != nil {
		return RetrievalContext{}, err
	}
	knowledge, err := retrieveKnowledgeTrigramPostgres(queryCtx, s.pool.Raw(), normalized, &remaining)
	if err != nil {
		return RetrievalContext{}, err
	}
	return RetrievalContext{PersonalMemories: memories, Knowledge: knowledge, SemanticStatus: string(semantic.StatusUnavailable)}, nil
}

func normalizePostgresSearchQuery(query string) (string, error) {
	return NormalizePostgresSearchQuery(query)
}

func retrieveKnowledgeTrigramPostgres(ctx context.Context, db Querier, query string, remaining *int) ([]RetrievedKnowledge, error) {
	return RetrieveKnowledgeTrigram(ctx, db, query, remaining)
}
