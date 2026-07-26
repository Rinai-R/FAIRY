package memory

import "context"

func (s *Store) CommitCompaction(conversationID string, expectedRevision uint64, summary string, contextWindow ContextWindowRecord, clearLane string) (CompactionResult, error) {
	return s.commitCompactionPostgres(context.Background(), conversationID, expectedRevision, summary, contextWindow, clearLane)
}

func (s *Store) CommitCompactionContext(ctx context.Context, conversationID string, expectedRevision uint64, summary string, contextWindow ContextWindowRecord, clearLane string) (CompactionResult, error) {
	return s.commitCompactionPostgres(ctx, conversationID, expectedRevision, summary, contextWindow, clearLane)
}

func (s *Store) CommitPromptWindow(conversationID string, expectedRevision uint64, summary string) (CompactionResult, error) {
	return s.CommitPromptWindowContext(context.Background(), conversationID, expectedRevision, summary)
}

func (s *Store) CommitPromptWindowContext(ctx context.Context, conversationID string, expectedRevision uint64, summary string) (CompactionResult, error) {
	return s.commitPromptWindowPostgres(ctx, conversationID, expectedRevision, summary)
}
