package compaction

import (
	"context"

	historyruntime "fairy/context/history/runtime"
)

func (s *Store) CommitCompaction(conversationID string, expectedRevision uint64, summary string, contextWindow historyruntime.ContextWindowRecord, clearLane string) (Result, error) {
	return s.commitCompactionPostgres(context.Background(), conversationID, expectedRevision, summary, contextWindow, clearLane)
}

func (s *Store) CommitCompactionContext(ctx context.Context, conversationID string, expectedRevision uint64, summary string, contextWindow historyruntime.ContextWindowRecord, clearLane string) (Result, error) {
	return s.commitCompactionPostgres(ctx, conversationID, expectedRevision, summary, contextWindow, clearLane)
}

func (s *Store) CommitPromptWindow(conversationID string, expectedRevision uint64, summary string) (Result, error) {
	return s.CommitPromptWindowContext(context.Background(), conversationID, expectedRevision, summary)
}

func (s *Store) CommitPromptWindowContext(ctx context.Context, conversationID string, expectedRevision uint64, summary string) (Result, error) {
	return s.commitPromptWindowPostgres(ctx, conversationID, expectedRevision, summary)
}
