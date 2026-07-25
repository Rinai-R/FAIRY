package postgres

import (
	"context"
	"errors"
	"fmt"

	domainmemory "fairy/internal/domain/memory"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	maxResultsPerKind        = 4
	maxRetrievedContextChars = domainmemory.MaxPersonalMemoryContentRunes
)

func ScanRetrievedPersonalMemories(rows pgx.Rows, remaining *int) ([]domainmemory.RetrievedPersonalMemory, error) {
	defer rows.Close()
	perKind := make(map[string]int)
	results := make([]domainmemory.RetrievedPersonalMemory, 0)
	for rows.Next() {
		var record domainmemory.RetrievedPersonalMemory
		var scopeKind string
		var character pgtype.Text
		var confidence int
		if err := rows.Scan(&record.ID, &record.Kind, &scopeKind, &character, &record.Content, &confidence, &record.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning retrieved personal memory: %w", err)
		}
		if perKind[record.Kind] >= maxResultsPerKind {
			continue
		}
		length := len([]rune(record.Content))
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("retrieved personal memory confidence is invalid")
		}
		if err := domainmemory.ValidatePersistedPersonalMemoryContent(record.ID, record.Content); err != nil {
			return nil, err
		}
		if length > *remaining {
			continue
		}
		*remaining -= length
		perKind[record.Kind]++
		record.Scope = domainmemory.MemoryScope{Type: scopeKind}
		if character.Valid {
			record.Scope.CharacterID = character.String
		}
		record.Layer = domainmemory.PersonalMemoryLayer(record.Kind, record.Scope)
		record.ConfidenceBasisPoints = uint16(confidence)
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating retrieved personal memories: %w", err)
	}
	return results, nil
}

func RetrievePersonalTrigram(ctx context.Context, db Querier, characterID, query string, remaining *int) ([]domainmemory.RetrievedPersonalMemory, error) {
	rows, err := db.Query(ctx, `
SELECT id, kind, scope_kind, character_id, content, confidence_basis_points, updated_at_ms
FROM personal_memories
WHERE status = 'active' AND review_status = 'ready'
  AND (scope_kind = 'global' OR (scope_kind = 'character' AND character_id = $1))
  AND (content ILIKE '%' || $2 || '%' OR content OPERATOR(public.%) $2 OR $2 OPERATOR(public.<%) content)
ORDER BY GREATEST(public.similarity(content, $2), public.word_similarity($2, content)) DESC,
         confidence_basis_points DESC,
         updated_at_ms DESC,
         id ASC
LIMIT 64`, characterID, query)
	if err != nil {
		return nil, fmt.Errorf("querying retrieved personal memories: %w", err)
	}
	return ScanRetrievedPersonalMemories(rows, remaining)
}

func RetrievePersonalExtractionProjection(
	ctx context.Context,
	db Querier,
	characterID string,
	projection []string,
	remaining *int,
	normalizeQuery func(string) (string, error),
) ([]domainmemory.RetrievedPersonalMemory, error) {
	queries := make([]string, 0, len(projection))
	projectionRunes := 0
	for _, fragment := range projection {
		normalized, err := normalizeQuery(fragment)
		if err != nil {
			return nil, err
		}
		if normalized == "" {
			continue
		}
		if len(queries) > 0 {
			projectionRunes++
		}
		projectionRunes += len([]rune(normalized))
		if projectionRunes > domainmemory.MaxFTSQueryChars {
			return nil, errors.New("extraction retrieval projection exceeds query budget")
		}
		queries = append(queries, normalized)
	}
	if len(queries) == 0 {
		return []domainmemory.RetrievedPersonalMemory{}, nil
	}
	rows, err := db.Query(ctx, `
WITH projection(query) AS (
  SELECT unnest($2::text[])
)
SELECT m.id, m.kind, m.scope_kind, m.character_id, m.content, m.confidence_basis_points, m.updated_at_ms
FROM personal_memories m
JOIN projection p ON (
  m.content ILIKE '%' || p.query || '%'
  OR m.content OPERATOR(public.%) p.query
  OR p.query OPERATOR(public.<%) m.content
)
WHERE m.status = 'active' AND m.review_status = 'ready'
  AND (m.scope_kind = 'global' OR (m.scope_kind = 'character' AND m.character_id = $1))
GROUP BY m.id, m.kind, m.scope_kind, m.character_id, m.content, m.confidence_basis_points, m.updated_at_ms
ORDER BY MAX(GREATEST(public.similarity(m.content, p.query), public.word_similarity(p.query, m.content))) DESC,
         m.confidence_basis_points DESC,
         m.updated_at_ms DESC,
         m.id ASC
LIMIT 64`, characterID, queries)
	if err != nil {
		return nil, fmt.Errorf("querying extraction existing personal memories: %w", err)
	}
	return ScanRetrievedPersonalMemories(rows, remaining)
}
