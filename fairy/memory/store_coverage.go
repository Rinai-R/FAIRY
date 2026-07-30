package memory

import (
	"context"
	"fmt"
)

func (s *Store) loadCommittedMemoryCoveragePostgres(ctx context.Context, conversationID string) ([]MemoryContextCoverage, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
WITH covered_turns AS (
  SELECT
    conversation_id,
    turn_id,
    min(memory_id) AS memory_id,
    min(result_status) AS result_status,
    min(created_at_ms) AS created_at_ms
  FROM memory_context_coverages
  WHERE conversation_id = $1
  GROUP BY conversation_id, turn_id
)
SELECT
  covered.conversation_id,
  covered.turn_id,
  covered.memory_id,
  covered.result_status,
  turn.sequence,
  min(message.sequence),
  max(message.sequence),
  GREATEST(1, (sum(char_length(message.content)) + 3) / 4),
  covered.created_at_ms
FROM covered_turns AS covered
JOIN conversation_turns AS turn
  ON turn.id = covered.turn_id
 AND turn.conversation_id = covered.conversation_id
JOIN conversation_messages AS message
  ON message.turn_id = covered.turn_id
 AND message.conversation_id = covered.conversation_id
WHERE turn.status = 'completed'
GROUP BY covered.conversation_id, covered.turn_id, covered.memory_id,
         covered.result_status, turn.sequence, covered.created_at_ms
HAVING count(*) = 2
ORDER BY turn.sequence, covered.turn_id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("loading committed memory coverage: %w", err)
	}
	defer rows.Close()
	records := make([]MemoryContextCoverage, 0)
	for rows.Next() {
		var record MemoryContextCoverage
		var turnSequence, startSequence, endSequence, coveredTokens int64
		if err := rows.Scan(
			&record.ConversationID, &record.TurnID, &record.MemoryID,
			&record.ResultStatus, &turnSequence, &startSequence, &endSequence,
			&coveredTokens, &record.CreatedAtUnixMS,
		); err != nil {
			return nil, fmt.Errorf("scanning committed memory coverage: %w", err)
		}
		record.TurnSequence = uint64(turnSequence)
		record.StartMessageSequence = uint64(startSequence)
		record.EndMessageSequence = uint64(endSequence)
		record.CoveredTokens = uint64(coveredTokens)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating committed memory coverage: %w", err)
	}
	return records, nil
}
