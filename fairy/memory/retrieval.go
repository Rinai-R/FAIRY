package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

func NormalizePostgresSearchQuery(query string) (string, error) {
	usable, err := BuildFTSQuery(query)
	if err != nil {
		return "", err
	}
	if usable == "" {
		return "", nil
	}
	return strings.Join(strings.Fields(query), " "), nil
}

// BuildFTSQuery mirrors Rust build_fts_query: alphanumeric runs → trigrams → quoted OR terms.
func BuildFTSQuery(query string) (string, error) {
	if len([]rune(query)) > MaxFTSQueryChars {
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

func RetrieveKnowledgeTrigram(ctx context.Context, db Querier, query string, remaining *int) ([]RetrievedKnowledge, error) {
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
		sources, err := KnowledgeSources(ctx, db, record.ID)
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
