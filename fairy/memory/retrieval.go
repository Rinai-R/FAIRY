package memory

import (
	"context"
	"errors"
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
	return s.RetrieveContext(context.Background(), characterID, query)
}

func (s *Store) RetrieveContext(ctx context.Context, characterID string, query string) (RetrievalContext, error) {
	return s.retrievePostgres(ctx, characterID, query)
}

// RetrievePublicKnowledgeContext is the public-surface retrieval boundary.
// It never queries personal memories or the mixed personal vector index.
func (s *Store) RetrievePublicKnowledgeContext(ctx context.Context, query string) (RetrievalContext, error) {
	if s == nil || s.pool == nil {
		return RetrievalContext{}, ErrDatabasePoolEmpty
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return RetrievalContext{}, err
	}
	result := RetrievalContext{
		PersonalMemories: []RetrievedPersonalMemory{},
		Knowledge:        []RetrievedKnowledge{},
		SemanticStatus:   string(semantic.StatusUnavailable),
	}
	if normalized == "" {
		return result, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	remaining := maxRetrievedContextChars
	result.Knowledge, err = retrieveKnowledgeTrigramPostgres(queryCtx, s.pool.Raw(), normalized, &remaining)
	if err != nil {
		return RetrievalContext{}, err
	}
	return result, nil
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
