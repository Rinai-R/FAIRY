package personal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"fairy/runtime/embedding"

	"github.com/pgvector/pgvector-go"
)

const seekDBRecentVectorWindow = 60 * time.Second

const personalMemorySeekDBScopeSQL = `status = 'active' AND review_status = 'ready'
  AND (scope_kind = 'global' OR (scope_kind = 'character' AND character_id = ?))`

const personalMemorySeekDBLiteralMatchSQL = `
  SELECT id, 1.0 AS score
  FROM personal_memories
  WHERE ` + personalMemorySeekDBScopeSQL + `
    AND LOCATE(LOWER(?), LOWER(content)) > 0`

const personalMemorySeekDBRankedSelectSQL = `
SELECT entry.id, entry.kind, entry.scope_kind, entry.character_id, entry.content,
       entry.confidence_basis_points, entry.updated_at_ms, ranked.score
FROM ranked_entries ranked
JOIN personal_memories entry ON entry.id = ranked.id
WHERE ` + personalMemorySeekDBScopeSQL + `
ORDER BY ranked.score DESC, entry.confidence_basis_points DESC,
         entry.updated_at_ms DESC, entry.id ASC
LIMIT ?`

const personalMemorySeekDBSearchSQL = `
WITH matching_entries AS (` + personalMemorySeekDBLiteralMatchSQL + `
  UNION ALL
  SELECT semantic.id, semantic.fts_score / (1.0 + semantic.fts_score) AS score
  FROM (
    SELECT id, MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) AS fts_score
    FROM personal_memories
    WHERE ` + personalMemorySeekDBScopeSQL + `
      AND MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) > 0
  ) semantic
),
ranked_entries AS (
  SELECT id, MAX(score) AS score
  FROM matching_entries
  GROUP BY id
)` + personalMemorySeekDBRankedSelectSQL

const personalMemorySeekDBLiteralSearchSQL = `
WITH matching_entries AS (` + personalMemorySeekDBLiteralMatchSQL + `
),
ranked_entries AS (
  SELECT id, MAX(score) AS score
  FROM matching_entries
  GROUP BY id
)` + personalMemorySeekDBRankedSelectSQL

const personalMemorySeekDBVectorColumnsSQL = `
id, kind, scope_kind, character_id, content,
confidence_basis_points, updated_at_ms,
GREATEST(0.0, LEAST(1.0, 1.0 - COSINE_DISTANCE(embedding, ?))) AS similarity`

const personalMemorySeekDBANNGlobalSearchSQL = `
SELECT ` + personalMemorySeekDBVectorColumnsSQL + `
FROM personal_memories FORCE INDEX (personal_memories_scope_status_idx)
WHERE scope_kind = 'global' AND character_id IS NULL
  AND review_status = 'ready' AND status = 'active'
  AND embedding IS NOT NULL
  AND embedding_space_id = ?
ORDER BY COSINE_DISTANCE(embedding, ?) APPROXIMATE
LIMIT ?`

const personalMemorySeekDBANNCharacterSearchSQL = `
SELECT ` + personalMemorySeekDBVectorColumnsSQL + `
FROM personal_memories FORCE INDEX (personal_memories_scope_status_idx)
WHERE scope_kind = 'character' AND character_id = ?
  AND review_status = 'ready' AND status = 'active'
  AND embedding IS NOT NULL
  AND embedding_space_id = ?
ORDER BY COSINE_DISTANCE(embedding, ?) APPROXIMATE
LIMIT ?`

const personalMemorySeekDBExactRecentSearchSQL = `
SELECT ` + personalMemorySeekDBVectorColumnsSQL + `
FROM personal_memories
WHERE ` + personalMemorySeekDBScopeSQL + `
  AND embedding_space_id = ?
  AND embedding IS NOT NULL
  AND embedding_content_hash = UNHEX(SHA2(content, 256))
  AND updated_at_ms >= ?
ORDER BY COSINE_DISTANCE(embedding, ?), id ASC
LIMIT ?`

const personalMemorySeekDBVectorSearchSQL = personalMemorySeekDBExactRecentSearchSQL

func personalMemorySeekDBSearchUsesLiteralOnly(query string) bool {
	return strings.ContainsAny(query, "%_")
}

func (s *Store) retrieveSeekDB(ctx context.Context, characterID, query string) (Retrieval, error) {
	if !s.usesSeekDB() {
		return Retrieval{}, ErrStoreBackendUnavailable
	}
	if err := validateID("character_id", characterID); err != nil {
		return Retrieval{}, err
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return Retrieval{}, err
	}
	textContext, err := s.retrieveSeekDBTextContext(ctx, characterID, normalized)
	if err != nil {
		return Retrieval{}, err
	}
	vectorLiteral, spaceID, semanticStatus, err := querySeekDBPersonalEmbedding(ctx, s.semanticEmbedderSnapshot(), query)
	if err != nil {
		return Retrieval{}, err
	}
	if vectorLiteral == "" {
		textContext.SemanticStatus = string(semanticStatus)
		return textContext, nil
	}
	vectorMatches, err := s.querySeekDBPersonalVectors(ctx, characterID, vectorLiteral, spaceID)
	if err != nil {
		return Retrieval{}, err
	}
	fused := fusePostgresRetrieval(textContext, vectorMatches)
	fused.SemanticStatus = string(embedding.SemanticStatusUsed)
	if len(vectorMatches) == 0 {
		fused.SemanticStatus = string(embedding.SemanticStatusReady)
	}
	return fused, nil
}

func (s *Store) retrieveSeekDBTextContext(ctx context.Context, characterID, normalized string) (Retrieval, error) {
	if normalized == "" {
		return Retrieval{SemanticStatus: string(embedding.SemanticStatusUnavailable)}, nil
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	searchSQL := personalMemorySeekDBSearchSQL
	args := []any{characterID, normalized, normalized, characterID, normalized, characterID, 64}
	if personalMemorySeekDBSearchUsesLiteralOnly(normalized) {
		searchSQL = personalMemorySeekDBLiteralSearchSQL
		args = []any{characterID, normalized, characterID, 64}
	}
	rows, err := s.seekDB.QueryContext(queryCtx, searchSQL, args...)
	if err != nil {
		return Retrieval{}, fmt.Errorf("querying SeekDB personal memories: %w", err)
	}
	defer rows.Close()
	remaining := MaxRetrievedContextChars
	memories, err := scanSeekDBRetrieved(rows, &remaining)
	if err != nil {
		return Retrieval{}, err
	}
	return Retrieval{PersonalMemories: memories, SemanticStatus: string(embedding.SemanticStatusUnavailable)}, nil
}

func (s *Store) querySeekDBPersonalVectors(
	ctx context.Context,
	characterID, vectorLiteral, spaceID string,
) (map[string]vectorPersonalTruth, error) {
	limit := MaxResultsPerKind * 2
	matches, err := s.querySeekDBPersonalVectorSQL(
		ctx, personalMemorySeekDBANNGlobalSearchSQL,
		vectorLiteral, spaceID, vectorLiteral, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB personal global ANN vectors: %w", err)
	}
	character, err := s.querySeekDBPersonalVectorSQL(
		ctx, personalMemorySeekDBANNCharacterSearchSQL,
		vectorLiteral, characterID, spaceID, vectorLiteral, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB personal character ANN vectors: %w", err)
	}
	maps.Copy(matches, character)
	recent, err := s.querySeekDBPersonalVectorSQL(
		ctx, personalMemorySeekDBExactRecentSearchSQL,
		vectorLiteral, characterID, spaceID, s.recentVectorCutoffMS(), vectorLiteral, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB personal exact recent vectors: %w", err)
	}
	maps.Copy(matches, recent)
	return matches, nil
}

func (s *Store) recentVectorCutoffMS() int64 {
	cutoff := s.currentUnixMS() - seekDBRecentVectorWindow.Milliseconds()
	if cutoff < 1 {
		return 1
	}
	return cutoff
}

func (s *Store) querySeekDBPersonalVectorSQL(
	ctx context.Context,
	searchSQL string,
	args ...any,
) (map[string]vectorPersonalTruth, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, searchSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]vectorPersonalTruth)
	for rows.Next() {
		record, similarity, err := scanSeekDBRetrievedRow(rows)
		if err != nil {
			return nil, err
		}
		if similarity < 0 || similarity > 1 {
			return nil, errors.New("personal memory vector similarity is invalid")
		}
		result[record.ID] = vectorPersonalTruth{record: record, score: similarity}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB personal memory vectors: %w", err)
	}
	return result, nil
}

func querySeekDBPersonalEmbedding(
	ctx context.Context,
	embedder embedding.SemanticEmbedder,
	query string,
) (vectorLiteral, spaceID string, status embedding.SemanticStatus, err error) {
	if embedder == nil || !embedder.Ready() {
		return "", "", embedding.SemanticStatusUnavailable, nil
	}
	if dims := embedder.Dims(); dims != embedding.Dimensions {
		return "", "", embedding.SemanticStatusUnavailable, fmt.Errorf("embedding dimensions = %d, want %d", dims, embedding.Dimensions)
	}
	semanticText := semanticQueryText(query)
	if semanticText == "" {
		return "", "", embedding.SemanticStatusReady, nil
	}
	spaceID, err = embedding.ModelID(embedder)
	if err != nil {
		return "", "", embedding.SemanticStatusUnavailable, err
	}
	var vectors [][]float32
	if contextual, ok := embedder.(embedding.ContextSemanticEmbedder); ok {
		vectors, err = contextual.EmbedContext(ctx, []string{semanticText})
	} else {
		vectors, err = embedder.Embed([]string{semanticText})
	}
	if err != nil {
		return "", "", embedding.SemanticStatusUnavailable, nil
	}
	if len(vectors) != 1 {
		return "", "", embedding.SemanticStatusUnavailable, fmt.Errorf("embedding result count = %d, want 1", len(vectors))
	}
	if err := embedding.ValidateVector(vectors[0]); err != nil {
		return "", "", embedding.SemanticStatusUnavailable, err
	}
	vector := pgvector.NewVector(vectors[0])
	return vector.String(), spaceID, embedding.SemanticStatusReady, nil
}

func scanSeekDBRetrieved(rows *sql.Rows, remaining *int) ([]Retrieved, error) {
	perKind := make(map[string]int)
	results := make([]Retrieved, 0)
	for rows.Next() {
		record, score, err := scanSeekDBRetrievedRow(rows)
		if err != nil {
			return nil, err
		}
		if perKind[record.Kind] >= MaxResultsPerKind {
			continue
		}
		if score <= 0 || score > 1 {
			return nil, errors.New("retrieved personal memory score is invalid")
		}
		length := len([]rune(record.Content))
		if length > *remaining {
			continue
		}
		*remaining -= length
		perKind[record.Kind]++
		record.TextScore = score
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB personal memories: %w", err)
	}
	return results, nil
}

func scanSeekDBRetrievedRow(rows *sql.Rows) (Retrieved, float64, error) {
	var (
		record     Retrieved
		scopeKind  string
		character  sql.NullString
		confidence int64
		score      float64
	)
	if err := rows.Scan(
		&record.ID, &record.Kind, &scopeKind, &character, &record.Content,
		&confidence, &record.UpdatedAtUnixMS, &score,
	); err != nil {
		return Retrieved{}, 0, fmt.Errorf("scanning SeekDB personal memory: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return Retrieved{}, 0, errors.New("retrieved personal memory confidence is invalid")
	}
	if err := ValidatePersistedContent(record.ID, record.Content); err != nil {
		return Retrieved{}, 0, err
	}
	record.Scope = Scope{Type: scopeKind}
	if character.Valid {
		record.Scope.CharacterID = character.String
	}
	record.Layer = Layer(record.Kind, record.Scope)
	record.ConfidenceBasisPoints = uint16(confidence)
	return record, score, nil
}
