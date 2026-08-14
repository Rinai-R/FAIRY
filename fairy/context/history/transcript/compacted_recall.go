package transcript

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCompactedTranscriptTurns   = 5
	MaxTranscriptRecallQueryRunes = 200
)

func (s *Store) SearchCompactedTranscript(
	ctx context.Context,
	conversationID string,
	cutoff uint64,
	query string,
	limit int,
) (CompactedTranscriptRecall, error) {
	query, err := validateCompactedTranscriptRecall(conversationID, cutoff, query, limit)
	if err != nil {
		return CompactedTranscriptRecall{}, err
	}
	if cutoff == 0 {
		return CompactedTranscriptRecall{Turns: []CompactedTranscriptTurn{}}, nil
	}
	if ctx == nil {
		return CompactedTranscriptRecall{}, errors.New("context is required")
	}
	if !s.usesSeekDB() {
		return CompactedTranscriptRecall{}, ErrStoreBackendUnavailable
	}
	return s.searchCompactedTranscriptSeekDB(ctx, conversationID, cutoff, query, limit)
}

func validateCompactedTranscriptRecall(conversationID string, cutoff uint64, query string, limit int) (string, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return "", err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("transcript recall query is required")
	}
	if utf8.RuneCountInString(query) > MaxTranscriptRecallQueryRunes {
		return "", errors.New("transcript recall query is too long or contains control characters")
	}
	for _, character := range query {
		if unicode.IsControl(character) {
			return "", errors.New("transcript recall query is too long or contains control characters")
		}
	}
	if limit < 1 || limit > MaxCompactedTranscriptTurns {
		return "", fmt.Errorf("transcript recall limit must be between 1 and %d", MaxCompactedTranscriptTurns)
	}
	if cutoff > math.MaxInt64 {
		return "", errors.New("transcript recall cutoff exceeds signed bigint")
	}
	return query, nil
}
