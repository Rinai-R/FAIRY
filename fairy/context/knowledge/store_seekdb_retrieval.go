package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	memoryretrieval "fairy/context/memory/retrieval"
	"fairy/runtime/embedding"

	"github.com/pgvector/pgvector-go"
)

const seekDBRecentVectorWindow = 60 * time.Second

const knowledgeSeekDBVectorColumnsSQL = `
entry.id, entry.topic, entry.statement, entry.verification_basis,
entry.confidence_basis_points, entry.source_url, entry.source_title,
entry.evidence_text, entry.source_fetched_at_ms, entry.updated_at_ms,
HEX(entry.embedding_content_hash),
GREATEST(0.0, LEAST(1.0, 1.0 - COSINE_DISTANCE(entry.embedding, ?))) AS similarity`

const knowledgeSeekDBANNSearchSQL = `
SELECT ` + knowledgeSeekDBVectorColumnsSQL + `
FROM knowledge_entries entry FORCE INDEX (knowledge_entries_status_updated_idx)
WHERE entry.status = 'verified'
  AND entry.embedding_space_id = ?
  AND entry.embedding IS NOT NULL
ORDER BY COSINE_DISTANCE(entry.embedding, ?) APPROXIMATE
LIMIT ?`

const knowledgeSeekDBExactRecentSearchSQL = `
SELECT ` + knowledgeSeekDBVectorColumnsSQL + `
FROM knowledge_entries entry
WHERE entry.status = 'verified'
  AND entry.embedding_space_id = ?
  AND entry.embedding IS NOT NULL
  AND entry.updated_at_ms >= ?
ORDER BY COSINE_DISTANCE(entry.embedding, ?), entry.id ASC
LIMIT ?`

func (s *Store) retrieveSeekDB(ctx context.Context, query string) (Retrieval, error) {
	if !s.usesSeekDB() {
		return Retrieval{}, ErrStoreBackendUnavailable
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return Retrieval{}, errors.New("knowledge search query is required")
	}
	text, err := s.searchForIngestSeekDB(ctx, query, MaxSearchCandidates)
	if err != nil {
		return Retrieval{}, err
	}
	vectorLiteral, spaceID, semanticStatus, err := querySeekDBKnowledgeEmbedding(ctx, s.semanticEmbedderSnapshot(), query)
	if err != nil {
		return Retrieval{}, err
	}
	if vectorLiteral == "" {
		return Retrieval{Entries: text, SemanticStatus: string(semanticStatus)}, nil
	}
	vectorMatches, err := s.querySeekDBKnowledgeVectors(ctx, vectorLiteral, spaceID)
	if err != nil {
		return Retrieval{}, err
	}
	if len(vectorMatches) == 0 {
		semanticStatus = embedding.SemanticStatusReady
	} else {
		semanticStatus = embedding.SemanticStatusUsed
	}
	return fuseSeekDBKnowledgeRetrieval(text, vectorMatches, semanticStatus), nil
}

func querySeekDBKnowledgeEmbedding(
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
	spaceID, err = embedding.ModelID(embedder)
	if err != nil {
		return "", "", embedding.SemanticStatusUnavailable, err
	}
	vectors, err := embedSeekDBQuery(ctx, embedder, query)
	if err != nil {
		return "", "", embedding.SemanticStatusUnavailable, nil
	}
	if err := embedding.ValidateVector(vectors[0]); err != nil {
		return "", "", embedding.SemanticStatusUnavailable, err
	}
	vector := pgvector.NewVector(vectors[0])
	return vector.String(), spaceID, embedding.SemanticStatusReady, nil
}

func embedSeekDBQuery(ctx context.Context, embedder embedding.SemanticEmbedder, query string) ([][]float32, error) {
	var (
		vectors [][]float32
		err     error
	)
	if contextual, ok := embedder.(embedding.ContextSemanticEmbedder); ok {
		vectors, err = contextual.EmbedContext(ctx, []string{query})
	} else {
		vectors, err = embedder.Embed([]string{query})
	}
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embedding result count = %d, want 1", len(vectors))
	}
	return vectors, nil
}

func (s *Store) querySeekDBKnowledgeVectors(ctx context.Context, vectorLiteral, spaceID string) (map[string]Retrieved, error) {
	limit := MaxSearchCandidates * 2
	matches, err := s.querySeekDBKnowledgeVectorSQL(
		ctx, knowledgeSeekDBANNSearchSQL,
		vectorLiteral, spaceID, vectorLiteral, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB knowledge ANN vectors: %w", err)
	}
	recent, err := s.querySeekDBKnowledgeVectorSQL(
		ctx, knowledgeSeekDBExactRecentSearchSQL,
		vectorLiteral, spaceID, s.recentVectorCutoffMS(), vectorLiteral, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB knowledge exact recent vectors: %w", err)
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

func (s *Store) querySeekDBKnowledgeVectorSQL(ctx context.Context, searchSQL string, args ...any) (map[string]Retrieved, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, searchSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSeekDBKnowledgeVectorRows(rows)
}

func scanSeekDBKnowledgeVectorRows(rows *sql.Rows) (map[string]Retrieved, error) {
	result := make(map[string]Retrieved)
	for rows.Next() {
		var (
			record          Retrieved
			confidence      int64
			sourceURL       sql.NullString
			sourceTitle     sql.NullString
			sourceEvidence  sql.NullString
			sourceFetchedAt sql.NullInt64
			hashHex         string
			similarity      float64
		)
		if err := rows.Scan(
			&record.ID, &record.Topic, &record.Statement, &record.VerificationBasis,
			&confidence, &sourceURL, &sourceTitle, &sourceEvidence, &sourceFetchedAt,
			&record.UpdatedAtUnixMS, &hashHex, &similarity,
		); err != nil {
			return nil, fmt.Errorf("scanning SeekDB knowledge vector: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("knowledge search confidence is invalid")
		}
		if similarity < 0 || similarity > 1 {
			return nil, errors.New("knowledge search score is invalid")
		}
		if strings.ToLower(hashHex) != embedding.ContentHash(record.Topic+"\n"+record.Statement) {
			continue
		}
		record.Layer = "knowledge"
		record.ConfidenceBasisPoints = uint16(confidence)
		record.TextScore = similarity
		if sourceURL.Valid {
			if !sourceEvidence.Valid || !sourceFetchedAt.Valid {
				return nil, errors.New("knowledge source projection is incomplete")
			}
			source := AssistantSource{
				URL: sourceURL.String, Snippet: sourceEvidence.String,
				Rank: 1, FetchedAtUnixMS: sourceFetchedAt.Int64,
			}
			if sourceTitle.Valid {
				source.Title = sourceTitle.String
			}
			record.Sources = []AssistantSource{source}
		} else if sourceTitle.Valid || sourceEvidence.Valid || sourceFetchedAt.Valid {
			return nil, errors.New("knowledge source projection is inconsistent")
		}
		result[record.ID] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB knowledge vectors: %w", err)
	}
	return result, nil
}

func fuseSeekDBKnowledgeRetrieval(
	text []Retrieved,
	vectorMatches map[string]Retrieved,
	status embedding.SemanticStatus,
) Retrieval {
	records := make(map[string]Retrieved, len(text)+len(vectorMatches))
	candidates := make([]memoryretrieval.Candidate, 0, len(text)+len(vectorMatches))
	for _, record := range text {
		records[record.ID] = record
		candidates = append(candidates, memoryretrieval.Candidate{
			ID: record.ID, Kind: "knowledge", TextScore: record.TextScore, HasText: true,
			UpdatedAtMS: record.UpdatedAtUnixMS, ConfidenceBP: record.ConfidenceBasisPoints,
		})
	}
	for id, record := range vectorMatches {
		records[id] = record
		candidates = append(candidates, memoryretrieval.Candidate{
			ID: id, Kind: "knowledge", VectorSim: record.TextScore, HasVector: true,
			UpdatedAtMS: record.UpdatedAtUnixMS, ConfidenceBP: record.ConfidenceBasisPoints,
		})
	}
	result := Retrieval{SemanticStatus: string(status)}
	for _, candidate := range memoryretrieval.Fuse(candidates, MaxSearchCandidates) {
		record, ok := records[candidate.ID]
		if !ok {
			continue
		}
		result.Entries = append(result.Entries, record)
	}
	return result
}
