package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"fairy/memory/semantic"
)

func (s *Store) retrievePostgres(ctx context.Context, characterID, query string) (RetrievalContext, error) {
	if err := validateID("character_id", characterID); err != nil {
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
	usable, err := buildFTSQuery(query)
	if err != nil {
		return "", err
	}
	if usable == "" {
		return "", nil
	}
	return strings.Join(strings.Fields(query), " "), nil
}

func retrieveKnowledgeTrigramPostgres(ctx context.Context, db postgresKnowledgeQuerier, query string, remaining *int) ([]RetrievedKnowledge, error) {
	rows, err := db.Query(ctx, `
SELECT id, topic, statement, verification_basis, confidence_basis_points, updated_at_ms
FROM knowledge_entries
WHERE status = 'verified'
  AND (
    topic ILIKE '%' || $1 || '%' OR statement ILIKE '%' || $1 || '%'
    OR topic OPERATOR(public.%) $1 OR statement OPERATOR(public.%) $1
    OR $1 OPERATOR(public.<%) topic OR $1 OPERATOR(public.<%) statement
  )
ORDER BY GREATEST(
           public.similarity(topic, $1), public.similarity(statement, $1),
           public.word_similarity($1, topic), public.word_similarity($1, statement)
         ) DESC,
         confidence_basis_points DESC,
         updated_at_ms DESC,
         id ASC
LIMIT $2`, query, maxResultsPerKind)
	if err != nil {
		return nil, fmt.Errorf("querying retrieved knowledge: %w", err)
	}
	defer rows.Close()
	results := make([]RetrievedKnowledge, 0)
	for rows.Next() {
		var record RetrievedKnowledge
		var confidence int
		if err := rows.Scan(&record.ID, &record.Topic, &record.Statement, &record.VerificationBasis, &confidence, &record.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning retrieved knowledge: %w", err)
		}
		length := len([]rune(record.Topic)) + len([]rune(record.Statement))
		if length > *remaining {
			continue
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("retrieved knowledge confidence is invalid")
		}
		*remaining -= length
		record.Layer = "knowledge"
		record.ConfidenceBasisPoints = uint16(confidence)
		sources, err := knowledgeSourcesPostgres(ctx, db, record.ID)
		if err != nil {
			return nil, err
		}
		record.Sources = sources
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating retrieved knowledge: %w", err)
	}
	return results, nil
}
