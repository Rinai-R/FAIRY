package memory

import (
	"context"
	"errors"
	"fmt"

	"fairy/memory/semantic"
	"fairy/vectorindex"

	"github.com/jackc/pgx/v5/pgtype"
)

type SemanticVectorIndex interface {
	Ready(context.Context) error
	Search(context.Context, []float32, string, string, int) ([]vectorindex.SearchHit, error)
}

type vectorPersonalTruth struct {
	record RetrievedPersonalMemory
	score  float64
}

type vectorKnowledgeTruth struct {
	record RetrievedKnowledge
	score  float64
}

func (s *Store) RetrieveWithSemanticVectorIndex(ctx context.Context, characterID, query string, embedder semantic.Embedder, index SemanticVectorIndex) (RetrievalContext, error) {
	if s == nil || s.pool == nil {
		return RetrievalContext{}, ErrDatabasePoolEmpty
	}
	if err := validateID("character_id", characterID); err != nil {
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
	if embedder == nil || !embedder.Ready() || index == nil {
		textContext.SemanticStatus = string(semantic.StatusUnavailable)
		return textContext, nil
	}
	if dims := embedder.Dims(); dims != SemanticEmbeddingDimensions {
		return RetrievalContext{}, fmt.Errorf("embedding dimensions = %d, want %d", dims, SemanticEmbeddingDimensions)
	}
	semanticText := semanticQueryText(query)
	if semanticText == "" {
		textContext.SemanticStatus = string(semantic.StatusReady)
		return textContext, nil
	}
	vectors, err := embedder.Embed([]string{semanticText})
	if err != nil {
		textContext.SemanticStatus = string(semantic.StatusUnavailable)
		return textContext, nil
	}
	if len(vectors) != 1 {
		return RetrievalContext{}, fmt.Errorf("embedder returned %d vectors for one retrieval query", len(vectors))
	}
	if err := vectorindex.ValidateVector(vectors[0]); err != nil {
		return RetrievalContext{}, err
	}
	hits, err := index.Search(ctx, vectors[0], SemanticEmbeddingModelID, characterID, maxResultsPerKind*2)
	if err != nil {
		textContext.SemanticStatus = string(semantic.StatusUnavailable)
		return textContext, nil
	}
	personalTruth, knowledgeTruth, err := s.truthCheckVectorHits(ctx, characterID, hits)
	if err != nil {
		return RetrievalContext{}, err
	}
	return fusePostgresRetrieval(textContext, personalTruth, knowledgeTruth), nil
}

func (s *Store) retrievePostgresTextContext(ctx context.Context, characterID, normalized string) (RetrievalContext, error) {
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

func (s *Store) truthCheckVectorHits(ctx context.Context, characterID string, hits []vectorindex.SearchHit) (map[string]vectorPersonalTruth, map[string]vectorKnowledgeTruth, error) {
	personalIDs := make([]string, 0)
	knowledgeIDs := make([]string, 0)
	for _, hit := range hits {
		switch hit.ItemKind {
		case vectorindex.ItemKindPersonalMemory:
			personalIDs = append(personalIDs, hit.ItemID)
		case vectorindex.ItemKindKnowledge:
			knowledgeIDs = append(knowledgeIDs, hit.ItemID)
		}
	}
	personal, err := s.truthCheckPersonalVectorHits(ctx, characterID, personalIDs)
	if err != nil {
		return nil, nil, err
	}
	knowledge, err := s.truthCheckKnowledgeVectorHits(ctx, knowledgeIDs)
	if err != nil {
		return nil, nil, err
	}
	validPersonal := make(map[string]vectorPersonalTruth)
	validKnowledge := make(map[string]vectorKnowledgeTruth)
	for _, hit := range hits {
		switch hit.ItemKind {
		case vectorindex.ItemKindPersonalMemory:
			truth, ok := personal[hit.ItemID]
			if ok && hit.ContentHash == semanticContentHash(truth.record.Content) {
				truth.score = hit.Score
				validPersonal[hit.ItemID] = truth
			}
		case vectorindex.ItemKindKnowledge:
			truth, ok := knowledge[hit.ItemID]
			if ok && hit.ContentHash == semanticContentHash(truth.record.Topic+"\n"+truth.record.Statement) {
				truth.score = hit.Score
				validKnowledge[hit.ItemID] = truth
			}
		}
	}
	return validPersonal, validKnowledge, nil
}

func (s *Store) truthCheckPersonalVectorHits(ctx context.Context, characterID string, ids []string) (map[string]vectorPersonalTruth, error) {
	result := make(map[string]vectorPersonalTruth)
	if len(ids) == 0 {
		return result, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT p.id, p.kind, p.scope_kind, p.character_id, p.content,
       p.confidence_basis_points, p.updated_at_ms
FROM personal_memories p
JOIN memory_embedding_items i
  ON i.item_kind = $2 AND i.item_id = p.id AND i.model_id = $3
WHERE p.id = ANY($1)
  AND i.status = 'embedded'
  AND p.status = 'active' AND p.review_status = 'ready'
  AND (p.scope_kind = 'global' OR (p.scope_kind = 'character' AND p.character_id = $4))`,
		ids, vectorindex.ItemKindPersonalMemory, SemanticEmbeddingModelID, characterID)
	if err != nil {
		return nil, fmt.Errorf("querying personal vector truth: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var record RetrievedPersonalMemory
		var scopeKind string
		var character pgtype.Text
		var confidence int
		if err := rows.Scan(&record.ID, &record.Kind, &scopeKind, &character, &record.Content, &confidence, &record.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning personal vector truth: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("personal vector truth confidence is invalid")
		}
		record.Scope = MemoryScope{Type: scopeKind}
		if character.Valid {
			record.Scope.CharacterID = character.String
		}
		record.Layer = personalMemoryLayer(record.Kind, record.Scope)
		record.ConfidenceBasisPoints = uint16(confidence)
		result[record.ID] = vectorPersonalTruth{record: record}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal vector truth: %w", err)
	}
	return result, nil
}

func (s *Store) truthCheckKnowledgeVectorHits(ctx context.Context, ids []string) (map[string]vectorKnowledgeTruth, error) {
	result := make(map[string]vectorKnowledgeTruth)
	if len(ids) == 0 {
		return result, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT k.id, k.topic, k.statement, k.verification_basis,
       k.confidence_basis_points, k.updated_at_ms
FROM knowledge_entries k
JOIN memory_embedding_items i
  ON i.item_kind = $2 AND i.item_id = k.id AND i.model_id = $3
WHERE k.id = ANY($1) AND i.status = 'embedded' AND k.status = 'verified'`,
		ids, vectorindex.ItemKindKnowledge, SemanticEmbeddingModelID)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge vector truth: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var record RetrievedKnowledge
		var confidence int
		if err := rows.Scan(&record.ID, &record.Topic, &record.Statement, &record.VerificationBasis, &confidence, &record.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning knowledge vector truth: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("knowledge vector truth confidence is invalid")
		}
		record.Layer = "knowledge"
		record.ConfidenceBasisPoints = uint16(confidence)
		record.Sources, err = knowledgeSourcesPostgres(queryCtx, s.pool.Raw(), record.ID)
		if err != nil {
			return nil, err
		}
		result[record.ID] = vectorKnowledgeTruth{record: record}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge vector truth: %w", err)
	}
	return result, nil
}

func fusePostgresRetrieval(text RetrievalContext, personal map[string]vectorPersonalTruth, knowledge map[string]vectorKnowledgeTruth) RetrievalContext {
	personalRecords := make(map[string]RetrievedPersonalMemory, len(text.PersonalMemories)+len(personal))
	personalCandidates := make([]semantic.Candidate, 0, len(text.PersonalMemories)+len(personal))
	for _, record := range text.PersonalMemories {
		personalRecords[record.ID] = record
		personalCandidates = append(personalCandidates, semantic.Candidate{ID: record.ID, Kind: record.Kind, FTSRank: 1, HasFTS: true, UpdatedAtMS: record.UpdatedAtUnixMS, ConfidenceBP: record.ConfidenceBasisPoints})
	}
	for id, truth := range personal {
		personalRecords[id] = truth.record
		personalCandidates = append(personalCandidates, semantic.Candidate{ID: id, Kind: truth.record.Kind, VectorSim: truth.score, HasVector: true, UpdatedAtMS: truth.record.UpdatedAtUnixMS, ConfidenceBP: truth.record.ConfidenceBasisPoints})
	}
	knowledgeRecords := make(map[string]RetrievedKnowledge, len(text.Knowledge)+len(knowledge))
	knowledgeCandidates := make([]semantic.Candidate, 0, len(text.Knowledge)+len(knowledge))
	for _, record := range text.Knowledge {
		knowledgeRecords[record.ID] = record
		knowledgeCandidates = append(knowledgeCandidates, semantic.Candidate{ID: record.ID, Kind: "knowledge", FTSRank: 1, HasFTS: true, UpdatedAtMS: record.UpdatedAtUnixMS, ConfidenceBP: record.ConfidenceBasisPoints})
	}
	for id, truth := range knowledge {
		knowledgeRecords[id] = truth.record
		knowledgeCandidates = append(knowledgeCandidates, semantic.Candidate{ID: id, Kind: "knowledge", VectorSim: truth.score, HasVector: true, UpdatedAtMS: truth.record.UpdatedAtUnixMS, ConfidenceBP: truth.record.ConfidenceBasisPoints})
	}

	remaining := maxRetrievedContextChars
	result := RetrievalContext{SemanticStatus: string(semantic.StatusUsed)}
	perKind := make(map[string]int)
	for _, candidate := range semantic.Fuse(personalCandidates, 64) {
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
	for _, candidate := range semantic.Fuse(knowledgeCandidates, maxResultsPerKind) {
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
		result.SemanticStatus = string(semantic.StatusReady)
	}
	return result
}
