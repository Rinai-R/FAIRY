package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"fairy/memory/semantic"
)

// Constants match crates/fairy-intelligence/src/store/mod.rs.
const (
	maxResultsPerKind        = 4
	maxRetrievedContextChars = 2400
	maxFTSQueryChars         = 2000
)

type RetrievedPersonalMemory struct {
	ID                    string      `json:"id"`
	Kind                  string      `json:"kind"`
	Layer                 string      `json:"layer"`
	Scope                 MemoryScope `json:"scope"`
	Content               string      `json:"content"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
	UpdatedAtUnixMS       int64       `json:"updatedAtUnixMs"`
}

type RetrievedKnowledge struct {
	ID                    string            `json:"id"`
	Layer                 string            `json:"layer"`
	Topic                 string            `json:"topic"`
	Statement             string            `json:"statement"`
	VerificationBasis     string            `json:"verificationBasis"`
	ConfidenceBasisPoints uint16            `json:"confidenceBasisPoints"`
	Sources               []AssistantSource `json:"sources"`
	UpdatedAtUnixMS       int64             `json:"updatedAtUnixMs"`
}

type RetrievalContext struct {
	PersonalMemories []RetrievedPersonalMemory `json:"personalMemories"`
	Knowledge        []RetrievedKnowledge      `json:"knowledge"`
	// SemanticStatus is non-secret metadata for callers; empty means legacy FTS-only.
	SemanticStatus string `json:"semanticStatus,omitempty"`
}

func (c RetrievalContext) Empty() bool {
	return len(c.PersonalMemories) == 0 && len(c.Knowledge) == 0
}

func (s *Store) Retrieve(characterID string, query string) (RetrievalContext, error) {
	if err := validateID("character_id", characterID); err != nil {
		return RetrievalContext{}, err
	}
	ftsQuery, err := buildFTSQuery(query)
	if err != nil {
		return RetrievalContext{}, err
	}
	if ftsQuery == "" {
		return RetrievalContext{SemanticStatus: string(semantic.StatusUnavailable)}, nil
	}
	db, err := s.openReadOnly()
	if err != nil {
		return RetrievalContext{}, err
	}
	defer db.Close()
	remaining := maxRetrievedContextChars
	memories, err := retrievePersonal(db, characterID, ftsQuery, &remaining)
	if err != nil {
		return RetrievalContext{}, err
	}
	knowledge, err := retrieveKnowledge(db, ftsQuery, &remaining)
	if err != nil {
		return RetrievalContext{}, err
	}
	return RetrievalContext{
		PersonalMemories: memories,
		Knowledge:        knowledge,
		SemanticStatus:   string(semantic.StatusUnavailable),
	}, nil
}

// RetrieveWithSemantic runs FTS plus sqlite-vec KNN when a ready embedder is
// explicitly injected. The default Retrieve path stays FTS-only until the app
// wires a real local embedder at the composition root.
func (s *Store) RetrieveWithSemantic(characterID string, query string, embedder semantic.Embedder) (RetrievalContext, error) {
	if err := validateID("character_id", characterID); err != nil {
		return RetrievalContext{}, err
	}
	ftsQuery, err := buildFTSQuery(query)
	if err != nil {
		return RetrievalContext{}, err
	}

	semanticStatus := string(semantic.StatusUnavailable)
	queryVector := ""
	if embedder != nil && embedder.Ready() {
		semanticStatus = string(semantic.StatusReady)
		if dims := embedder.Dims(); dims != SemanticEmbeddingDimensions {
			return RetrievalContext{}, fmt.Errorf("embedding dimensions = %d, want %d", dims, SemanticEmbeddingDimensions)
		}
		semanticText := semanticQueryText(query)
		if semanticText != "" {
			vectors, err := embedder.Embed([]string{semanticText})
			if err != nil {
				return RetrievalContext{}, fmt.Errorf("embedding retrieval query: %w", err)
			}
			if len(vectors) != 1 {
				return RetrievalContext{}, fmt.Errorf("embedder returned %d vectors for one retrieval query", len(vectors))
			}
			queryVector, err = sqliteVecLiteral(vectors[0])
			if err != nil {
				return RetrievalContext{}, err
			}
		}
	}
	if ftsQuery == "" && queryVector == "" {
		return RetrievalContext{SemanticStatus: semanticStatus}, nil
	}

	db, err := s.openReadOnly()
	if err != nil {
		return RetrievalContext{}, err
	}
	defer db.Close()

	remaining := maxRetrievedContextChars
	if queryVector == "" {
		memories, err := retrievePersonal(db, characterID, ftsQuery, &remaining)
		if err != nil {
			return RetrievalContext{}, err
		}
		knowledge, err := retrieveKnowledge(db, ftsQuery, &remaining)
		if err != nil {
			return RetrievalContext{}, err
		}
		return RetrievalContext{PersonalMemories: memories, Knowledge: knowledge, SemanticStatus: semanticStatus}, nil
	}

	personalCandidates, personalUsedVector, err := retrievePersonalSemanticCandidates(db, characterID, ftsQuery, queryVector)
	if err != nil {
		return RetrievalContext{}, err
	}
	knowledgeCandidates, knowledgeUsedVector, err := retrieveKnowledgeSemanticCandidates(db, ftsQuery, queryVector)
	if err != nil {
		return RetrievalContext{}, err
	}
	if personalUsedVector || knowledgeUsedVector {
		semanticStatus = string(semantic.StatusUsed)
	}
	memories, err := retrievePersonalByCandidates(db, characterID, semantic.Fuse(personalCandidates, 64), &remaining)
	if err != nil {
		return RetrievalContext{}, err
	}
	knowledge, err := retrieveKnowledgeByCandidates(db, semantic.Fuse(knowledgeCandidates, maxResultsPerKind), &remaining)
	if err != nil {
		return RetrievalContext{}, err
	}
	return RetrievalContext{PersonalMemories: memories, Knowledge: knowledge, SemanticStatus: semanticStatus}, nil
}

// buildFTSQuery mirrors Rust build_fts_query: alphanumeric runs → trigrams → quoted OR terms.
func buildFTSQuery(query string) (string, error) {
	if len([]rune(query)) > maxFTSQueryChars {
		return "", errors.New("retrieval query is too long or contains control characters")
	}
	for _, character := range query {
		if unicode.IsControl(character) {
			return "", errors.New("retrieval query is too long or contains control characters")
		}
	}
	terms := make(map[string]struct{})
	chunk := make([]rune, 0)
	flush := func() {
		if len(chunk) >= 3 {
			for index := 0; index+3 <= len(chunk); index++ {
				terms[string(chunk[index:index+3])] = struct{}{}
			}
		}
		chunk = chunk[:0]
	}
	for _, character := range query {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			chunk = append(chunk, character)
			continue
		}
		flush()
	}
	flush()
	if len(terms) == 0 {
		return "", nil
	}
	ordered := make([]string, 0, len(terms))
	for term := range terms {
		ordered = append(ordered, term)
	}
	sort.Strings(ordered)
	quoted := make([]string, 0, len(ordered))
	for _, term := range ordered {
		quoted = append(quoted, `"`+term+`"`)
	}
	return strings.Join(quoted, " OR "), nil
}

func semanticQueryText(query string) string {
	trimmed := strings.TrimSpace(query)
	if len([]rune(trimmed)) < 2 {
		return ""
	}
	return trimmed
}

type retrievalQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func retrievePersonalSemanticCandidates(db retrievalQuerier, characterID string, ftsQuery string, queryVector string) ([]semantic.Candidate, bool, error) {
	candidates := make([]semantic.Candidate, 0)
	if ftsQuery != "" {
		fts, err := retrievePersonalFTSCandidates(db, characterID, ftsQuery)
		if err != nil {
			return nil, false, err
		}
		candidates = append(candidates, fts...)
	}
	vector, err := retrievePersonalVectorCandidates(db, characterID, queryVector)
	if err != nil {
		return nil, false, err
	}
	candidates = append(candidates, vector...)
	return candidates, len(vector) > 0, nil
}

func retrievePersonalFTSCandidates(db retrievalQuerier, characterID string, ftsQuery string) ([]semantic.Candidate, error) {
	rows, err := db.Query(`
SELECT p.id, p.kind, bm25(personal_memories_fts),
       p.confidence_basis_points, p.updated_at_ms
FROM personal_memories_fts
JOIN personal_memories p ON p.rowid = personal_memories_fts.rowid
WHERE personal_memories_fts MATCH ?1
  AND p.status = 'active'
  AND p.review_status = 'ready'
  AND (
    p.scope_kind = 'global'
    OR (p.scope_kind = 'character' AND p.character_id = ?2)
  )
ORDER BY bm25(personal_memories_fts) ASC,
         p.confidence_basis_points DESC,
         p.updated_at_ms DESC,
         p.id ASC
LIMIT 64`, ftsQuery, characterID)
	if err != nil {
		return nil, fmt.Errorf("querying personal FTS candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]semantic.Candidate, 0)
	for rows.Next() {
		var id, kind string
		var rank float64
		var confidence int
		var updated int64
		if err := rows.Scan(&id, &kind, &rank, &confidence, &updated); err != nil {
			return nil, fmt.Errorf("scanning personal FTS candidate: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("personal FTS candidate confidence is invalid")
		}
		candidates = append(candidates, semantic.Candidate{
			ID:           id,
			Kind:         kind,
			FTSRank:      rank,
			HasFTS:       true,
			UpdatedAtMS:  updated,
			ConfidenceBP: uint16(confidence),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal FTS candidates: %w", err)
	}
	return candidates, nil
}

func retrievePersonalVectorCandidates(db retrievalQuerier, characterID string, queryVector string) ([]semantic.Candidate, error) {
	rows, err := db.Query(`
SELECT p.id, p.kind, memory_embedding_vec.distance,
       p.confidence_basis_points, p.updated_at_ms
FROM memory_embedding_vec
JOIN memory_embedding_items i ON i.vector_rowid = memory_embedding_vec.rowid
JOIN personal_memories p ON p.id = i.item_id
WHERE memory_embedding_vec.embedding MATCH ?1
  AND memory_embedding_vec.k = 64
  AND i.item_kind = 'personal_memory'
  AND i.model_id = ?2
  AND i.dimensions = ?3
  AND i.status = 'embedded'
  AND p.status = 'active'
  AND p.review_status = 'ready'
  AND (
    p.scope_kind = 'global'
    OR (p.scope_kind = 'character' AND p.character_id = ?4)
  )
ORDER BY memory_embedding_vec.distance ASC,
         p.confidence_basis_points DESC,
         p.updated_at_ms DESC,
         p.id ASC
LIMIT 64`, queryVector, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, characterID)
	if err != nil {
		return nil, fmt.Errorf("querying personal vector candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]semantic.Candidate, 0)
	for rows.Next() {
		candidate, err := scanVectorCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning personal vector candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal vector candidates: %w", err)
	}
	return candidates, nil
}

func retrieveKnowledgeSemanticCandidates(db retrievalQuerier, ftsQuery string, queryVector string) ([]semantic.Candidate, bool, error) {
	candidates := make([]semantic.Candidate, 0)
	if ftsQuery != "" {
		fts, err := retrieveKnowledgeFTSCandidates(db, ftsQuery)
		if err != nil {
			return nil, false, err
		}
		candidates = append(candidates, fts...)
	}
	vector, err := retrieveKnowledgeVectorCandidates(db, queryVector)
	if err != nil {
		return nil, false, err
	}
	candidates = append(candidates, vector...)
	return candidates, len(vector) > 0, nil
}

func retrieveKnowledgeFTSCandidates(db retrievalQuerier, ftsQuery string) ([]semantic.Candidate, error) {
	rows, err := db.Query(`
SELECT k.id, bm25(knowledge_entries_fts),
       k.confidence_basis_points, k.updated_at_ms
FROM knowledge_entries_fts
JOIN knowledge_entries k ON k.rowid = knowledge_entries_fts.rowid
WHERE knowledge_entries_fts MATCH ?1 AND k.status = 'verified'
ORDER BY bm25(knowledge_entries_fts) ASC,
         k.confidence_basis_points DESC,
         k.updated_at_ms DESC,
         k.id ASC
LIMIT 64`, ftsQuery)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge FTS candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]semantic.Candidate, 0)
	for rows.Next() {
		var id string
		var rank float64
		var confidence int
		var updated int64
		if err := rows.Scan(&id, &rank, &confidence, &updated); err != nil {
			return nil, fmt.Errorf("scanning knowledge FTS candidate: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("knowledge FTS candidate confidence is invalid")
		}
		candidates = append(candidates, semantic.Candidate{
			ID:           id,
			Kind:         "knowledge",
			FTSRank:      rank,
			HasFTS:       true,
			UpdatedAtMS:  updated,
			ConfidenceBP: uint16(confidence),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge FTS candidates: %w", err)
	}
	return candidates, nil
}

func retrieveKnowledgeVectorCandidates(db retrievalQuerier, queryVector string) ([]semantic.Candidate, error) {
	rows, err := db.Query(`
SELECT k.id, 'knowledge', memory_embedding_vec.distance,
       k.confidence_basis_points, k.updated_at_ms
FROM memory_embedding_vec
JOIN memory_embedding_items i ON i.vector_rowid = memory_embedding_vec.rowid
JOIN knowledge_entries k ON k.id = i.item_id
WHERE memory_embedding_vec.embedding MATCH ?1
  AND memory_embedding_vec.k = 64
  AND i.item_kind = 'knowledge'
  AND i.model_id = ?2
  AND i.dimensions = ?3
  AND i.status = 'embedded'
  AND k.status = 'verified'
ORDER BY memory_embedding_vec.distance ASC,
         k.confidence_basis_points DESC,
         k.updated_at_ms DESC,
         k.id ASC
LIMIT 64`, queryVector, SemanticEmbeddingModelID, SemanticEmbeddingDimensions)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge vector candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]semantic.Candidate, 0)
	for rows.Next() {
		candidate, err := scanVectorCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning knowledge vector candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge vector candidates: %w", err)
	}
	return candidates, nil
}

type vectorCandidateScanner interface {
	Scan(dest ...any) error
}

func scanVectorCandidate(scanner vectorCandidateScanner) (semantic.Candidate, error) {
	var id, kind string
	var distance float64
	var confidence int
	var updated int64
	if err := scanner.Scan(&id, &kind, &distance, &confidence, &updated); err != nil {
		return semantic.Candidate{}, err
	}
	if confidence < 0 || confidence > 10000 {
		return semantic.Candidate{}, errors.New("vector candidate confidence is invalid")
	}
	return semantic.Candidate{
		ID:           id,
		Kind:         kind,
		VectorSim:    1 / (1 + math.Max(0, distance)),
		HasVector:    true,
		UpdatedAtMS:  updated,
		ConfidenceBP: uint16(confidence),
	}, nil
}

func retrievePersonal(db retrievalQuerier, characterID string, ftsQuery string, remaining *int) ([]RetrievedPersonalMemory, error) {
	rows, err := db.Query(`
SELECT p.id, p.kind, p.scope_kind, p.character_id,
       p.content, p.confidence_basis_points, p.updated_at_ms
FROM personal_memories_fts f
JOIN personal_memories p ON p.rowid = f.rowid
WHERE personal_memories_fts MATCH ?1
  AND p.status = 'active'
  AND p.review_status = 'ready'
  AND (
    p.scope_kind = 'global'
    OR (p.scope_kind = 'character' AND p.character_id = ?2)
  )
ORDER BY bm25(personal_memories_fts) ASC,
         p.confidence_basis_points DESC,
         p.updated_at_ms DESC,
         p.id ASC
LIMIT 64`, ftsQuery, characterID)
	if err != nil {
		return nil, fmt.Errorf("querying retrieved personal memories: %w", err)
	}
	defer rows.Close()
	results := make([]RetrievedPersonalMemory, 0)
	perKind := make(map[string]int)
	for rows.Next() {
		var id, kind, scopeKind, content string
		var memoryCharacter sql.NullString
		var confidence int
		var updated int64
		if err := rows.Scan(&id, &kind, &scopeKind, &memoryCharacter, &content, &confidence, &updated); err != nil {
			return nil, fmt.Errorf("scanning retrieved personal memory: %w", err)
		}
		if perKind[kind] >= maxResultsPerKind {
			continue
		}
		length := len([]rune(content))
		if length > *remaining {
			continue
		}
		*remaining -= length
		perKind[kind]++
		scope := MemoryScope{Type: scopeKind}
		if memoryCharacter.Valid {
			scope.CharacterID = memoryCharacter.String
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("retrieved personal memory confidence is invalid")
		}
		results = append(results, RetrievedPersonalMemory{
			ID:                    id,
			Kind:                  kind,
			Layer:                 personalMemoryLayer(kind, scope),
			Scope:                 scope,
			Content:               content,
			ConfidenceBasisPoints: uint16(confidence),
			UpdatedAtUnixMS:       updated,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating retrieved personal memories: %w", err)
	}
	return results, nil
}

func retrieveKnowledge(db retrievalQuerier, ftsQuery string, remaining *int) ([]RetrievedKnowledge, error) {
	rows, err := db.Query(`
SELECT k.id, k.topic, k.statement, k.verification_basis,
       k.confidence_basis_points, k.updated_at_ms
FROM knowledge_entries_fts f
JOIN knowledge_entries k ON k.rowid = f.rowid
WHERE knowledge_entries_fts MATCH ?1 AND k.status = 'verified'
ORDER BY bm25(knowledge_entries_fts) ASC,
         k.confidence_basis_points DESC,
         k.updated_at_ms DESC,
         k.id ASC
LIMIT ?2`, ftsQuery, maxResultsPerKind)
	if err != nil {
		return nil, fmt.Errorf("querying retrieved knowledge: %w", err)
	}
	defer rows.Close()
	results := make([]RetrievedKnowledge, 0)
	for rows.Next() {
		var record RetrievedKnowledge
		var confidence int
		if err := rows.Scan(
			&record.ID, &record.Topic, &record.Statement, &record.VerificationBasis, &confidence, &record.UpdatedAtUnixMS,
		); err != nil {
			return nil, fmt.Errorf("scanning retrieved knowledge: %w", err)
		}
		length := len([]rune(record.Topic)) + len([]rune(record.Statement))
		if length > *remaining {
			continue
		}
		*remaining -= length
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("retrieved knowledge confidence is invalid")
		}
		record.Layer = "knowledge"
		record.ConfidenceBasisPoints = uint16(confidence)
		sources, err := knowledgeSources(db, record.ID)
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

func retrievePersonalByCandidates(db *sql.DB, characterID string, candidates []semantic.Candidate, remaining *int) ([]RetrievedPersonalMemory, error) {
	results := make([]RetrievedPersonalMemory, 0)
	perKind := make(map[string]int)
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID == "" {
			continue
		}
		if _, ok := seen[candidate.ID]; ok {
			continue
		}
		seen[candidate.ID] = struct{}{}
		record, ok, err := personalMemoryByCandidateID(db, characterID, candidate.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if perKind[record.Kind] >= maxResultsPerKind {
			continue
		}
		length := len([]rune(record.Content))
		if length > *remaining {
			continue
		}
		*remaining -= length
		perKind[record.Kind]++
		results = append(results, record)
	}
	return results, nil
}

func personalMemoryByCandidateID(db *sql.DB, characterID string, id string) (RetrievedPersonalMemory, bool, error) {
	row := db.QueryRow(`
SELECT p.id, p.kind, p.scope_kind, p.character_id,
       p.content, p.confidence_basis_points, p.updated_at_ms
FROM personal_memories p
WHERE p.id = ?1
  AND p.status = 'active'
  AND p.review_status = 'ready'
  AND (
    p.scope_kind = 'global'
    OR (p.scope_kind = 'character' AND p.character_id = ?2)
  )`, id, characterID)
	var record RetrievedPersonalMemory
	var scopeKind string
	var memoryCharacter sql.NullString
	var confidence int
	if err := row.Scan(&record.ID, &record.Kind, &scopeKind, &memoryCharacter, &record.Content, &confidence, &record.UpdatedAtUnixMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RetrievedPersonalMemory{}, false, nil
		}
		return RetrievedPersonalMemory{}, false, fmt.Errorf("reading retrieved personal memory candidate: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return RetrievedPersonalMemory{}, false, errors.New("retrieved personal memory candidate confidence is invalid")
	}
	record.Scope = MemoryScope{Type: scopeKind}
	if memoryCharacter.Valid {
		record.Scope.CharacterID = memoryCharacter.String
	}
	record.Layer = personalMemoryLayer(record.Kind, record.Scope)
	record.ConfidenceBasisPoints = uint16(confidence)
	return record, true, nil
}

func retrieveKnowledgeByCandidates(db *sql.DB, candidates []semantic.Candidate, remaining *int) ([]RetrievedKnowledge, error) {
	results := make([]RetrievedKnowledge, 0)
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID == "" {
			continue
		}
		if _, ok := seen[candidate.ID]; ok {
			continue
		}
		seen[candidate.ID] = struct{}{}
		record, ok, err := knowledgeByCandidateID(db, candidate.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		length := len([]rune(record.Topic)) + len([]rune(record.Statement))
		if length > *remaining {
			continue
		}
		*remaining -= length
		results = append(results, record)
	}
	return results, nil
}

func knowledgeByCandidateID(db *sql.DB, id string) (RetrievedKnowledge, bool, error) {
	row := db.QueryRow(`
SELECT k.id, k.topic, k.statement, k.verification_basis,
       k.confidence_basis_points, k.updated_at_ms
FROM knowledge_entries k
WHERE k.id = ?1 AND k.status = 'verified'`, id)
	var record RetrievedKnowledge
	var confidence int
	if err := row.Scan(&record.ID, &record.Topic, &record.Statement, &record.VerificationBasis, &confidence, &record.UpdatedAtUnixMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RetrievedKnowledge{}, false, nil
		}
		return RetrievedKnowledge{}, false, fmt.Errorf("reading retrieved knowledge candidate: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return RetrievedKnowledge{}, false, errors.New("retrieved knowledge candidate confidence is invalid")
	}
	record.Layer = "knowledge"
	record.ConfidenceBasisPoints = uint16(confidence)
	sources, err := knowledgeSources(db, record.ID)
	if err != nil {
		return RetrievedKnowledge{}, false, err
	}
	record.Sources = sources
	return record, true, nil
}

func personalMemoryLayer(kind string, scope MemoryScope) string {
	switch strings.TrimSpace(kind) {
	case "profile":
		return "profile"
	case "preference":
		return "preference"
	case "experience":
		return "experience"
	case "relationship":
		return "relationship"
	}
	if scope.Type == "character" {
		return "relationship"
	}
	return "memory"
}
