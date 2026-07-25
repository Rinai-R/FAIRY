package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	domainmemory "fairy/internal/domain/memory"

	"github.com/jackc/pgx/v5"
)

func ListConversationMessagesBefore(ctx context.Context, db ConversationDB, conversationID string, beforeSequence uint64, limit int) (domainmemory.MessagePage, error) {
	var exists int
	if err := db.QueryRow(ctx, "SELECT 1 FROM conversations WHERE id = $1", conversationID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return domainmemory.MessagePage{}, errors.New("conversation does not exist")
	} else if err != nil {
		return domainmemory.MessagePage{}, fmt.Errorf("checking conversation for message page: %w", err)
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
		return domainmemory.MessagePage{}, fmt.Errorf("listing conversation messages: %w", err)
	}
	defer rows.Close()
	messages := make([]domainmemory.MessageRecord, 0, limit+1)
	for rows.Next() {
		message, err := ScanMessageRecord(rows)
		if err != nil {
			return domainmemory.MessagePage{}, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return domainmemory.MessagePage{}, fmt.Errorf("iterating conversation message page: %w", err)
	}
	var next *uint64
	if len(messages) > limit {
		messages = messages[:limit]
		cursor := messages[len(messages)-1].Sequence
		next = &cursor
	}
	slices.Reverse(messages)
	return domainmemory.MessagePage{Messages: messages, NextBeforeSequence: next}, nil
}
