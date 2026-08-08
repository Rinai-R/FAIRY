package personal

import (
	"context"
	"errors"
	"fmt"

	memoryretrieval "fairy/context/memory/retrieval"
	"fairy/runtime/embedding"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"
)

type vectorPersonalTruth struct {
	record Retrieved
	score  float64
}

type semanticQueryEmbedding struct {
	Vector  *pgvector.Vector
	ModelID string
	Status  embedding.SemanticStatus
}

func (s *Store) retrievePostgresHybrid(ctx context.Context, characterID, query string) (Retrieval, error) {
	if s == nil || s.pool == nil {
		return Retrieval{}, ErrDatabasePoolEmpty
	}
	if err := validateID("character_id", characterID); err != nil {
		return Retrieval{}, err
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return Retrieval{}, err
	}
	textContext, err := s.retrievePostgresTextContext(ctx, characterID, normalized)
	if err != nil {
		return Retrieval{}, err
	}
	queryEmbedding, err := s.queryEmbedding(query)
	if err != nil {
		return Retrieval{}, err
	}
	if queryEmbedding.Vector == nil {
		textContext.SemanticStatus = string(queryEmbedding.Status)
		return textContext, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	personal, err := retrievePersonalVectorPostgres(queryCtx, s.pool.Raw(), characterID, queryEmbedding.ModelID, *queryEmbedding.Vector)
	if err != nil {
		return Retrieval{}, err
	}
	return fusePostgresRetrieval(textContext, personal), nil
}

func (s *Store) queryEmbedding(query string) (semanticQueryEmbedding, error) {
	return queryEmbeddingWithSnapshot(s.semanticEmbedderSnapshot(), query, false)
}

func queryEmbeddingWithSnapshot(embedder embedding.SemanticEmbedder, query string, failOnProviderError bool) (semanticQueryEmbedding, error) {
	if embedder == nil || !embedder.Ready() {
		return semanticQueryEmbedding{Status: embedding.SemanticStatusUnavailable}, nil
	}
	if dims := embedder.Dims(); dims != embedding.Dimensions {
		return semanticQueryEmbedding{Status: embedding.SemanticStatusUnavailable}, fmt.Errorf("embedding dimensions = %d, want %d", dims, embedding.Dimensions)
	}
	semanticText := semanticQueryText(query)
	if semanticText == "" {
		return semanticQueryEmbedding{Status: embedding.SemanticStatusReady}, nil
	}
	modelID, err := embedding.ModelID(embedder)
	if err != nil {
		return semanticQueryEmbedding{Status: embedding.SemanticStatusUnavailable}, err
	}
	vectors, err := embedder.Embed([]string{semanticText})
	if err != nil {
		if failOnProviderError {
			return semanticQueryEmbedding{Status: embedding.SemanticStatusUnavailable}, fmt.Errorf("embedding knowledge ingest recall: %w", err)
		}
		return semanticQueryEmbedding{Status: embedding.SemanticStatusUnavailable}, nil
	}
	if len(vectors) != 1 {
		return semanticQueryEmbedding{Status: embedding.SemanticStatusUnavailable}, fmt.Errorf("embedding result count = %d, want 1", len(vectors))
	}
	if err := embedding.ValidateVector(vectors[0]); err != nil {
		return semanticQueryEmbedding{Status: embedding.SemanticStatusUnavailable}, err
	}
	vector := pgvector.NewVector(vectors[0])
	return semanticQueryEmbedding{Vector: &vector, ModelID: modelID, Status: embedding.SemanticStatusReady}, nil
}

func (s *Store) retrievePostgresTextContext(ctx context.Context, characterID, normalized string) (Retrieval, error) {
	if normalized == "" {
		return Retrieval{SemanticStatus: string(embedding.SemanticStatusUnavailable)}, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	remaining := MaxRetrievedContextChars
	memories, err := RetrieveTrigram(queryCtx, s.pool.Raw(), characterID, normalized, &remaining)
	if err != nil {
		return Retrieval{}, err
	}
	return Retrieval{PersonalMemories: memories, SemanticStatus: string(embedding.SemanticStatusUnavailable)}, nil
}

func retrievePersonalVectorPostgres(ctx context.Context, db DatabaseQuerier, characterID, modelID string, queryVector pgvector.Vector) (map[string]vectorPersonalTruth, error) {
	rows, err := db.Query(ctx, `
SELECT id, kind, scope_kind, character_id, content,
       confidence_basis_points, updated_at_ms,
       GREATEST(0.0, LEAST(1.0, 1.0 - (embedding_v2 OPERATOR(public.<=>) $1::public.vector))) AS similarity
FROM personal_memories
WHERE status = 'active'
  AND review_status = 'ready'
  AND embedding_model_id_v2 = $2
  AND embedding_content_hash_v2 = encode(sha256(convert_to(content, 'UTF8')), 'hex')
  AND embedding_v2 IS NOT NULL
  AND (
    scope_kind = 'global'
    OR (scope_kind = 'character' AND character_id = $3)
  )
ORDER BY embedding_v2 OPERATOR(public.<=>) $1::public.vector, id ASC
LIMIT $4`, queryVector.String(), modelID, characterID, MaxResultsPerKind*2)
	if err != nil {
		return nil, fmt.Errorf("querying personal memory vectors: %w", err)
	}
	defer rows.Close()
	result := make(map[string]vectorPersonalTruth)
	for rows.Next() {
		var record Retrieved
		var scopeKind string
		var character pgtype.Text
		var confidence int
		var similarity float64
		if err := rows.Scan(&record.ID, &record.Kind, &scopeKind, &character, &record.Content, &confidence, &record.UpdatedAtUnixMS, &similarity); err != nil {
			return nil, fmt.Errorf("scanning personal memory vector: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("personal memory vector confidence is invalid")
		}
		if err := ValidatePersistedContent(record.ID, record.Content); err != nil {
			return nil, err
		}
		record.Scope = Scope{Type: scopeKind}
		if character.Valid {
			record.Scope.CharacterID = character.String
		}
		record.Layer = Layer(record.Kind, record.Scope)
		record.ConfidenceBasisPoints = uint16(confidence)
		result[record.ID] = vectorPersonalTruth{record: record, score: similarity}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal memory vectors: %w", err)
	}
	return result, nil
}

func fusePostgresRetrieval(text Retrieval, vectorMatches map[string]vectorPersonalTruth) Retrieval {
	personalRecords := make(map[string]Retrieved, len(text.PersonalMemories)+len(vectorMatches))
	personalCandidates := make([]memoryretrieval.Candidate, 0, len(text.PersonalMemories)+len(vectorMatches))
	for _, record := range text.PersonalMemories {
		personalRecords[record.ID] = record
		personalCandidates = append(personalCandidates, memoryretrieval.Candidate{ID: record.ID, Kind: record.Kind, TextScore: record.TextScore, HasText: true, UpdatedAtMS: record.UpdatedAtUnixMS, ConfidenceBP: record.ConfidenceBasisPoints})
	}
	for id, truth := range vectorMatches {
		personalRecords[id] = truth.record
		personalCandidates = append(personalCandidates, memoryretrieval.Candidate{ID: id, Kind: truth.record.Kind, VectorSim: truth.score, HasVector: true, UpdatedAtMS: truth.record.UpdatedAtUnixMS, ConfidenceBP: truth.record.ConfidenceBasisPoints})
	}
	remaining := MaxRetrievedContextChars
	result := Retrieval{SemanticStatus: string(embedding.SemanticStatusUsed)}
	perKind := make(map[string]int)
	for _, candidate := range memoryretrieval.Fuse(personalCandidates, 64) {
		record, ok := personalRecords[candidate.ID]
		if !ok || perKind[record.Kind] >= MaxResultsPerKind {
			continue
		}
		length := len([]rune(record.Content))
		if length > remaining {
			continue
		}
		remaining -= length
		perKind[record.Kind]++
		result.PersonalMemories = append(result.PersonalMemories, record)
	}
	if len(vectorMatches) == 0 {
		result.SemanticStatus = string(embedding.SemanticStatusReady)
	}
	return result
}
