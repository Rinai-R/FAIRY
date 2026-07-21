package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/jackc/pgx/v5"
)

func (s *Store) listConversationMessagesBeforePostgres(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var exists int
	if err := s.pool.Raw().QueryRow(queryCtx, "SELECT 1 FROM conversations WHERE id = $1", conversationID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return MessagePage{}, errors.New("conversation does not exist")
	} else if err != nil {
		return MessagePage{}, fmt.Errorf("checking conversation for message page: %w", err)
	}
	before := int64(math.MaxInt64)
	if beforeSequence != 0 {
		before = int64(beforeSequence)
	}
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT id, conversation_id, turn_id, sequence, role, content, created_at_ms
FROM conversation_messages
WHERE conversation_id = $1 AND sequence < $2
ORDER BY sequence DESC
LIMIT $3`, conversationID, before, limit+1)
	if err != nil {
		return MessagePage{}, fmt.Errorf("listing conversation messages: %w", err)
	}
	defer rows.Close()
	messages := make([]MessageRecord, 0, limit+1)
	for rows.Next() {
		var message MessageRecord
		var sequence int64
		if err := rows.Scan(&message.ID, &message.ConversationID, &message.TurnID, &sequence, &message.Role, &message.Content, &message.CreatedAtUnixMS); err != nil {
			return MessagePage{}, fmt.Errorf("scanning conversation message page: %w", err)
		}
		message.Sequence = uint64(sequence)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return MessagePage{}, fmt.Errorf("iterating conversation message page: %w", err)
	}
	var next *uint64
	if len(messages) > limit {
		messages = messages[:limit]
		cursor := messages[len(messages)-1].Sequence
		next = &cursor
	}
	slices.Reverse(messages)
	return MessagePage{Messages: messages, NextBeforeSequence: next}, nil
}
