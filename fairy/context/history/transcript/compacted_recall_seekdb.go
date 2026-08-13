package transcript

import (
	"context"
	"errors"
	"fmt"
)

const compactedTranscriptRecallSeekDBSQL = `
WITH matching_messages AS (
  SELECT m.turn_id, m.sequence, 1.0 AS score
  FROM conversation_messages m
  WHERE m.conversation_id = ?
    AND m.sequence <= ?
    AND LOCATE(LOWER(?), LOWER(m.content)) > 0
  UNION ALL
  SELECT semantic.turn_id, semantic.sequence,
         semantic.fts_score / (1.0 + semantic.fts_score) AS score
  FROM (
    SELECT m.turn_id, m.sequence,
           MATCH(m.content) AGAINST(? IN NATURAL LANGUAGE MODE) AS fts_score
    FROM conversation_messages m
    WHERE m.conversation_id = ?
      AND m.sequence <= ?
      AND MATCH(m.content) AGAINST(? IN NATURAL LANGUAGE MODE) > 0
  ) semantic
),
matched_turns AS (
  SELECT turn_id, MAX(score) AS score, MAX(sequence) AS newest_sequence
  FROM matching_messages
  GROUP BY turn_id
  ORDER BY score DESC, newest_sequence DESC, turn_id ASC
  LIMIT ?
)
SELECT matched.score,
       m.id, COALESCE(t.message_id, ''), m.conversation_id, m.turn_id,
       m.sequence, m.role, m.content, m.expression_parts, m.created_at_ms
FROM matched_turns matched
JOIN conversation_messages m
  ON m.conversation_id = ?
 AND m.turn_id = matched.turn_id
 AND m.sequence <= ?
JOIN conversation_turns t
  ON t.id = m.turn_id AND t.conversation_id = m.conversation_id
ORDER BY matched.score DESC, matched.newest_sequence DESC,
         matched.turn_id ASC, m.sequence ASC`

func (s *Store) searchCompactedTranscriptSeekDB(
	ctx context.Context,
	conversationID string,
	cutoff uint64,
	query string,
	limit int,
) (CompactedTranscriptRecall, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return CompactedTranscriptRecall{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(
		queryCtx,
		compactedTranscriptRecallSeekDBSQL,
		conversationID,
		int64(cutoff),
		query,
		query,
		conversationID,
		int64(cutoff),
		query,
		limit+1,
		conversationID,
		int64(cutoff),
	)
	if err != nil {
		return CompactedTranscriptRecall{}, fmt.Errorf("searching SeekDB compacted conversation transcript: %w", err)
	}
	defer rows.Close()

	turns := make([]CompactedTranscriptTurn, 0, limit+1)
	for rows.Next() {
		var (
			score               float64
			message             MessageRecord
			sequence            int64
			expressionPartsJSON []byte
		)
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
			return CompactedTranscriptRecall{}, fmt.Errorf("scanning SeekDB compacted transcript recall: %w", err)
		}
		if score <= 0 || score > 1 {
			return CompactedTranscriptRecall{}, errors.New("stored SeekDB compacted transcript score is invalid")
		}
		if err := validateSeekDBIdentifier("stored message_id", message.ID); err != nil {
			return CompactedTranscriptRecall{}, err
		}
		if err := validateSeekDBIdentifier("stored message conversation_id", message.ConversationID); err != nil {
			return CompactedTranscriptRecall{}, err
		}
		if message.ConversationID != conversationID {
			return CompactedTranscriptRecall{}, errors.New("stored SeekDB compacted transcript escaped its conversation")
		}
		if err := validateSeekDBIdentifier("stored turn_id", message.TurnID); err != nil {
			return CompactedTranscriptRecall{}, err
		}
		if err := ValidateOptionalMessageID(message.MessageID); err != nil {
			return CompactedTranscriptRecall{}, fmt.Errorf("validating stored SeekDB external message id: %w", err)
		}
		if sequence <= 0 || uint64(sequence) > cutoff || message.CreatedAtUnixMS < 0 {
			return CompactedTranscriptRecall{}, errors.New("stored SeekDB compacted transcript message is invalid")
		}
		message, err = finishScannedMessage(message, sequence, expressionPartsJSON)
		if err != nil {
			return CompactedTranscriptRecall{}, err
		}
		if len(turns) == 0 || turns[len(turns)-1].TurnID != message.TurnID {
			turns = append(turns, CompactedTranscriptTurn{
				TurnID:   message.TurnID,
				Score:    score,
				Messages: []MessageRecord{},
			})
		} else if turns[len(turns)-1].Score != score {
			return CompactedTranscriptRecall{}, errors.New("stored SeekDB compacted transcript turn score is inconsistent")
		}
		turns[len(turns)-1].Messages = append(turns[len(turns)-1].Messages, message)
	}
	if err := rows.Err(); err != nil {
		return CompactedTranscriptRecall{}, fmt.Errorf("iterating SeekDB compacted transcript recall: %w", err)
	}

	truncated := len(turns) > limit
	if truncated {
		turns = turns[:limit]
	}
	return CompactedTranscriptRecall{Turns: turns, Truncated: truncated}, nil
}
