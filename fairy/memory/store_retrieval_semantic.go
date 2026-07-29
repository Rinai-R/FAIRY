package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"
)

type vectorPersonalTruth struct {
	record RetrievedPersonalMemory
	score  float64
}

type vectorKnowledgeTruth struct {
	record RetrievedKnowledge
	score  float64
}

func (s *Store) retrievePostgresHybrid(ctx context.Context, characterID, query string) (RetrievalContext, error) {
	if s == nil || s.pool == nil {
		return RetrievalContext{}, ErrDatabasePoolEmpty
	}
	if err := ValidateID("character_id", characterID); err != nil {
		return RetrievalContext{}, err
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return RetrievalContext{}, err
	}
	textContext, err := s.retrievePostgresTextContext(ctx, characterID, normalized)
	if err != nil {
		return RetrievalContext{}, err
	}
	queryVector, semanticStatus, err := s.queryEmbedding(query)
	if err != nil {
		return RetrievalContext{}, err
	}
	if queryVector == nil {
		textContext.SemanticStatus = string(semanticStatus)
		return textContext, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	personal, err := retrievePersonalVectorPostgres(queryCtx, s.pool.Raw(), characterID, *queryVector)
	if err != nil {
		return RetrievalContext{}, err
	}
	knowledge, err := retrieveKnowledgeVectorPostgres(queryCtx, s.pool.Raw(), *queryVector)
	if err != nil {
		return RetrievalContext{}, err
	}
	return fusePostgresRetrieval(textContext, personal, knowledge), nil
}

func (s *Store) retrievePublicKnowledgePostgres(ctx context.Context, query string) (RetrievalContext, error) {
	if s == nil || s.pool == nil {
		return RetrievalContext{}, ErrDatabasePoolEmpty
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return RetrievalContext{}, err
	}
	textContext := RetrievalContext{
		PersonalMemories: []RetrievedPersonalMemory{},
		Knowledge:        []RetrievedKnowledge{},
		SemanticStatus:   string(SemanticStatusUnavailable),
	}
	if normalized != "" {
		queryCtx, cancel := s.pool.QueryContext(ctx)
		remaining := maxRetrievedContextChars
		textContext.Knowledge, err = retrieveKnowledgeTrigramPostgres(queryCtx, s.pool.Raw(), normalized, &remaining)
		cancel()
		if err != nil {
			return RetrievalContext{}, err
		}
	}
	queryVector, semanticStatus, err := s.queryEmbedding(query)
	if err != nil {
		return RetrievalContext{}, err
	}
	if queryVector == nil {
		textContext.SemanticStatus = string(semanticStatus)
		return textContext, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	knowledge, err := retrieveKnowledgeVectorPostgres(queryCtx, s.pool.Raw(), *queryVector)
	if err != nil {
		return RetrievalContext{}, err
	}
	return fusePostgresRetrieval(textContext, nil, knowledge), nil
}

func (s *Store) queryEmbedding(query string) (*pgvector.Vector, SemanticStatus, error) {
	if s.semanticEmbedder == nil || !s.semanticEmbedder.Ready() {
		return nil, SemanticStatusUnavailable, nil
	}
	if dims := s.semanticEmbedder.Dims(); dims != SemanticEmbeddingDimensions {
		return nil, SemanticStatusUnavailable, fmt.Errorf("embedding dimensions = %d, want %d", dims, SemanticEmbeddingDimensions)
	}
	semanticText := semanticQueryText(query)
	if semanticText == "" {
		return nil, SemanticStatusReady, nil
	}
	vectors, err := s.semanticEmbedder.Embed([]string{semanticText})
	if err != nil {
		return nil, SemanticStatusUnavailable, nil
	}
	if len(vectors) != 1 {
		return nil, SemanticStatusUnavailable, fmt.Errorf("embedding result count = %d, want 1", len(vectors))
	}
	if err := ValidateVector(vectors[0]); err != nil {
		return nil, SemanticStatusUnavailable, err
	}
	vector := pgvector.NewVector(vectors[0])
	return &vector, SemanticStatusReady, nil
}

func (s *Store) retrievePostgresTextContext(ctx context.Context, characterID, normalized string) (RetrievalContext, error) {
	if normalized == "" {
		return RetrievalContext{SemanticStatus: string(SemanticStatusUnavailable)}, nil
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
	return RetrievalContext{PersonalMemories: memories, Knowledge: knowledge, SemanticStatus: string(SemanticStatusUnavailable)}, nil
}

func retrievePersonalVectorPostgres(ctx context.Context, db Querier, characterID string, queryVector pgvector.Vector) (map[string]vectorPersonalTruth, error) {
	rows, err := db.Query(ctx, `
SELECT id, kind, scope_kind, character_id, content,
       confidence_basis_points, updated_at_ms,
       GREATEST(0.0, LEAST(1.0, 1.0 - (embedding OPERATOR(public.<=>) $1::public.vector))) AS similarity
FROM personal_memories
WHERE status = 'active'
  AND review_status = 'ready'
  AND embedding_model_id = $2
  AND embedding IS NOT NULL
  AND (
    scope_kind = 'global'
    OR (scope_kind = 'character' AND character_id = $3)
  )
ORDER BY embedding OPERATOR(public.<=>) $1::public.vector, id ASC
LIMIT $4`, queryVector.String(), SemanticEmbeddingModelID, characterID, maxResultsPerKind*2)
	if err != nil {
		return nil, fmt.Errorf("querying personal memory vectors: %w", err)
	}
	defer rows.Close()
	result := make(map[string]vectorPersonalTruth)
	for rows.Next() {
		var record RetrievedPersonalMemory
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
		if err := ValidatePersistedPersonalMemoryContent(record.ID, record.Content); err != nil {
			return nil, err
		}
		record.Scope = MemoryScope{Type: scopeKind}
		if character.Valid {
			record.Scope.CharacterID = character.String
		}
		record.Layer = PersonalMemoryLayer(record.Kind, record.Scope)
		record.ConfidenceBasisPoints = uint16(confidence)
		result[record.ID] = vectorPersonalTruth{record: record, score: similarity}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal memory vectors: %w", err)
	}
	return result, nil
}

func retrieveKnowledgeVectorPostgres(ctx context.Context, db ConversationDB, queryVector pgvector.Vector) (map[string]vectorKnowledgeTruth, error) {
	rows, err := db.Query(ctx, `
SELECT id, topic, statement, verification_basis,
       confidence_basis_points, updated_at_ms,
       GREATEST(0.0, LEAST(1.0, 1.0 - (embedding OPERATOR(public.<=>) $1::public.vector))) AS similarity
FROM knowledge_entries
WHERE status = 'verified'
  AND embedding_model_id = $2
  AND embedding IS NOT NULL
ORDER BY embedding OPERATOR(public.<=>) $1::public.vector, id ASC
LIMIT $3`, queryVector.String(), SemanticEmbeddingModelID, maxResultsPerKind*2)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge vectors: %w", err)
	}
	result := make(map[string]vectorKnowledgeTruth)
	for rows.Next() {
		var record RetrievedKnowledge
		var confidence int
		var similarity float64
		if err := rows.Scan(&record.ID, &record.Topic, &record.Statement, &record.VerificationBasis, &confidence, &record.UpdatedAtUnixMS, &similarity); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning knowledge vector: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			rows.Close()
			return nil, errors.New("knowledge vector confidence is invalid")
		}
		record.Layer = "knowledge"
		record.ConfidenceBasisPoints = uint16(confidence)
		result[record.ID] = vectorKnowledgeTruth{record: record, score: similarity}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating knowledge vectors: %w", err)
	}
	rows.Close()
	for id, truth := range result {
		sources, err := knowledgeSourcesPostgres(ctx, db, id)
		if err != nil {
			return nil, err
		}
		truth.record.Sources = sources
		result[id] = truth
	}
	return result, nil
}

func fusePostgresRetrieval(text RetrievalContext, personal map[string]vectorPersonalTruth, knowledge map[string]vectorKnowledgeTruth) RetrievalContext {
	personalRecords := make(map[string]RetrievedPersonalMemory, len(text.PersonalMemories)+len(personal))
	personalCandidates := make([]SemanticCandidate, 0, len(text.PersonalMemories)+len(personal))
	for _, record := range text.PersonalMemories {
		personalRecords[record.ID] = record
		personalCandidates = append(personalCandidates, SemanticCandidate{ID: record.ID, Kind: record.Kind, TextScore: record.TextScore, HasText: true, UpdatedAtMS: record.UpdatedAtUnixMS, ConfidenceBP: record.ConfidenceBasisPoints})
	}
	for id, truth := range personal {
		personalRecords[id] = truth.record
		personalCandidates = append(personalCandidates, SemanticCandidate{ID: id, Kind: truth.record.Kind, VectorSim: truth.score, HasVector: true, UpdatedAtMS: truth.record.UpdatedAtUnixMS, ConfidenceBP: truth.record.ConfidenceBasisPoints})
	}
	knowledgeRecords := make(map[string]RetrievedKnowledge, len(text.Knowledge)+len(knowledge))
	knowledgeCandidates := make([]SemanticCandidate, 0, len(text.Knowledge)+len(knowledge))
	for _, record := range text.Knowledge {
		knowledgeRecords[record.ID] = record
		knowledgeCandidates = append(knowledgeCandidates, SemanticCandidate{ID: record.ID, Kind: "knowledge", TextScore: record.TextScore, HasText: true, UpdatedAtMS: record.UpdatedAtUnixMS, ConfidenceBP: record.ConfidenceBasisPoints})
	}
	for id, truth := range knowledge {
		knowledgeRecords[id] = truth.record
		knowledgeCandidates = append(knowledgeCandidates, SemanticCandidate{ID: id, Kind: "knowledge", VectorSim: truth.score, HasVector: true, UpdatedAtMS: truth.record.UpdatedAtUnixMS, ConfidenceBP: truth.record.ConfidenceBasisPoints})
	}

	remaining := maxRetrievedContextChars
	result := RetrievalContext{SemanticStatus: string(SemanticStatusUsed)}
	perKind := make(map[string]int)
	for _, candidate := range FuseSemanticCandidates(personalCandidates, 64) {
		record, ok := personalRecords[candidate.ID]
		if !ok || perKind[record.Kind] >= maxResultsPerKind {
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
	for _, candidate := range FuseSemanticCandidates(knowledgeCandidates, maxResultsPerKind) {
		record, ok := knowledgeRecords[candidate.ID]
		if !ok {
			continue
		}
		length := len([]rune(record.Topic)) + len([]rune(record.Statement))
		if length > remaining {
			continue
		}
		remaining -= length
		result.Knowledge = append(result.Knowledge, record)
	}
	if len(personal) == 0 && len(knowledge) == 0 {
		result.SemanticStatus = string(SemanticStatusReady)
	}
	return result
}
