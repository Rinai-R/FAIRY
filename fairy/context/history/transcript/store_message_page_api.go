package transcript

import (
	"context"
	"errors"
	"math"
)

func (s *Store) ListConversationMessagesBefore(conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	return s.ListConversationMessagesBeforeContext(context.Background(), conversationID, beforeSequence, limit)
}

func (s *Store) ListConversationMessagesBeforeContext(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return MessagePage{}, err
	}
	if beforeSequence > math.MaxInt64 {
		return MessagePage{}, errors.New("beforeSequence is invalid")
	}
	if limit <= 0 || limit > MaxMessagePageLimit {
		return MessagePage{}, errors.New("message page limit must be between 1 and 200")
	}
	if !s.usesSeekDB() {
		return MessagePage{}, ErrStoreBackendUnavailable
	}
	return s.listConversationMessagesBeforeSeekDB(ctx, conversationID, beforeSequence, limit)
}
