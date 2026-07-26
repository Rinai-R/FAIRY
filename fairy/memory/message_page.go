package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/jackc/pgx/v5"
)

func ListConversationMessagesBefore(ctx context.Context, db ConversationDB, conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	var exists int
	if err := db.QueryRow(ctx, "SELECT 1 FROM conversations WHERE id = $1", conversationID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return MessagePage{}, errors.New("conversation does not exist")
	} else if err != nil {
		return MessagePage{}, fmt.Errorf("checking conversation for message page: %w", err)
	}
	before := int64(math.MaxInt64)
	if beforeSequence != 0 {
		before = int64(beforeSequence)
	}
	rows, err := db.Query(ctx, `
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
		message, err := ScanMessageRecord(rows)
		if err != nil {
			return MessagePage{}, err
		}
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
