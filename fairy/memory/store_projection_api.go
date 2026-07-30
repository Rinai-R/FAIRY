package memory

import "context"

func (s *Store) CommitPromptProjection(
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	projection PromptProjectionState,
	contextWindow ContextWindowRecord,
	clearLane string,
) (CompactionResult, error) {
	return s.CommitPromptProjectionContext(
		context.Background(), conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		projection, contextWindow, clearLane,
	)
}

func (s *Store) CommitPromptProjectionContext(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	projection PromptProjectionState,
	contextWindow ContextWindowRecord,
	clearLane string,
) (CompactionResult, error) {
	return s.commitPromptProjectionPostgres(
		ctx, conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		projection, contextWindow, clearLane,
	)
}
