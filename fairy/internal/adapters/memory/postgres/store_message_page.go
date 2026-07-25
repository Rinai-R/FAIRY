package postgres

import (
	"context"
)

func (s *Store) listConversationMessagesBeforePostgres(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return ListConversationMessagesBefore(queryCtx, s.pool.Raw(), conversationID, beforeSequence, limit)
}
