package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Constants match crates/fairy-intelligence/src/store/mod.rs.
const (
	maxResultsPerKind         = 4
	maxRetrievedContextChars  = 2400
	maxFTSQueryChars          = 2000
)

type RetrievedPersonalMemory struct {
	ID                    string      `json:"id"`
	Kind                  string      `json:"kind"`
	Scope                 MemoryScope `json:"scope"`
	Content               string      `json:"content"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
	UpdatedAtUnixMS       int64       `json:"updatedAtUnixMs"`
}

type RetrievedKnowledge struct {
	ID                    string            `json:"id"`
	Topic                 string            `json:"topic"`
	Statement             string            `json:"statement"`
	VerificationBasis     string            `json:"verificationBasis"`
	ConfidenceBasisPoints uint16            `json:"confidenceBasisPoints"`
	Sources               []AssistantSource `json:"sources"`
	UpdatedAtUnixMS       int64             `json:"updatedAtUnixMs"`
}

type RetrievalContext struct {
	PersonalMemories []RetrievedPersonalMemory `json:"personalMemories"`
	Knowledge        []RetrievedKnowledge     `json:"knowledge"`
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
		return RetrievalContext{}, nil
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
	return RetrievalContext{PersonalMemories: memories, Knowledge: knowledge}, nil
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

type retrievalQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
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
