package memory

import (
	"context"
	"errors"
	"math"
)

const (
	DefaultMessagePageLimit = 50
	MaxMessagePageLimit     = 200
)

type MessagePage struct {
	Messages           []MessageRecord `json:"messages"`
	NextBeforeSequence *uint64         `json:"nextBeforeSequence,omitempty"`
}

func (s *Store) ListConversationMessagesBefore(conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	return s.ListConversationMessagesBeforeContext(context.Background(), conversationID, beforeSequence, limit)
}

func (s *Store) ListConversationMessagesBeforeContext(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return MessagePage{}, err
	}
	if beforeSequence > math.MaxInt64 {
		return MessagePage{}, errors.New("beforeSequence is invalid")
	}
	if limit <= 0 || limit > MaxMessagePageLimit {
		return MessagePage{}, errors.New("message page limit must be between 1 and 200")
	}
	return s.listConversationMessagesBeforePostgres(ctx, conversationID, beforeSequence, limit)
}
