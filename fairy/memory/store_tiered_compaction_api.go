package memory

import "context"

func (s *Store) CommitTieredCompaction(
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	summary string,
	cutoff uint64,
	projection PromptProjectionState,
	contextWindow ContextWindowRecord,
	clearLane string,
) (CompactionResult, error) {
	return s.CommitTieredCompactionContext(
		context.Background(), conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		summary, cutoff, projection, contextWindow, clearLane,
	)
}

func (s *Store) CommitTieredCompactionContext(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	summary string,
	cutoff uint64,
	projection PromptProjectionState,
	contextWindow ContextWindowRecord,
	clearLane string,
) (CompactionResult, error) {
	return s.commitTieredCompactionPostgres(
		ctx, conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		summary, cutoff, projection, contextWindow, clearLane,
	)
}
