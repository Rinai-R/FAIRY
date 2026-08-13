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
	if s != nil && s.usesSeekDB() {
		return s.searchCompactedTranscriptSeekDB(ctx, conversationID, cutoff, query, limit)
	}
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return CompactedTranscriptRecall{}, ErrDatabasePoolEmpty
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, compactedTranscriptRecallSQL, conversationID, int64(cutoff), query, limit+1)
	if err != nil {
		return CompactedTranscriptRecall{}, fmt.Errorf("searching compacted conversation transcript: %w", err)
	}
	defer rows.Close()

	turns := make([]CompactedTranscriptTurn, 0, limit+1)
	for rows.Next() {
		var score float64
		var message MessageRecord
		var sequence int64
		var expressionPartsJSON []byte
		if err := rows.Scan(
			&score,
			&message.ID,
			&message.MessageID,
			&message.ConversationID,
			&message.TurnID,
			&sequence,
			&message.Role,
			&message.Content,
			&expressionPartsJSON,
			&message.CreatedAtUnixMS,
		); err != nil {
			return CompactedTranscriptRecall{}, fmt.Errorf("scanning compacted transcript recall: %w", err)
		}
		message, err = finishScannedMessage(message, sequence, expressionPartsJSON)
		if err != nil {
			return CompactedTranscriptRecall{}, err
		}
		if len(turns) == 0 || turns[len(turns)-1].TurnID != message.TurnID {
			turns = append(turns, CompactedTranscriptTurn{TurnID: message.TurnID, Score: score, Messages: []MessageRecord{}})
		}
		turns[len(turns)-1].Messages = append(turns[len(turns)-1].Messages, message)
	}
	if err := rows.Err(); err != nil {
		return CompactedTranscriptRecall{}, fmt.Errorf("iterating compacted transcript recall: %w", err)
	}

	truncated := len(turns) > limit
	if truncated {
		turns = turns[:limit]
	}
	return CompactedTranscriptRecall{Turns: turns, Truncated: truncated}, nil
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

const compactedTranscriptRecallSQL = `
WITH matched_turns AS (
  SELECT m.turn_id,
         MAX(CASE
           WHEN strpos(lower(m.content), lower($3)) > 0 THEN 1.0
           ELSE GREATEST(public.similarity(m.content, $3), public.word_similarity($3, m.content))
         END) AS score,
         MAX(m.sequence) AS newest_sequence
  FROM conversation_messages m
  WHERE m.conversation_id = $1
    AND m.sequence <= $2
    AND (
      strpos(lower(m.content), lower($3)) > 0
      OR m.content OPERATOR(public.%) $3
      OR $3 OPERATOR(public.<%) m.content
    )
  GROUP BY m.turn_id
  ORDER BY score DESC, newest_sequence DESC, m.turn_id ASC
  LIMIT $4
)
SELECT matched.score,
       m.id, COALESCE(t.message_id, ''), m.conversation_id, m.turn_id,
       m.sequence, m.role, m.content, m.expression_parts, m.created_at_ms
FROM matched_turns matched
JOIN conversation_messages m ON m.conversation_id = $1 AND m.turn_id = matched.turn_id AND m.sequence <= $2
JOIN conversation_turns t ON t.id = m.turn_id AND t.conversation_id = m.conversation_id
ORDER BY matched.score DESC, matched.newest_sequence DESC, matched.turn_id ASC, m.sequence ASC`
