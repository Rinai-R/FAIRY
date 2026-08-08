package compaction

import (
	"context"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
)

func (s *Store) CommitPromptProjection(
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
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
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	return s.commitPromptProjectionPostgres(
		ctx, conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		projection, contextWindow, clearLane,
	)
}
