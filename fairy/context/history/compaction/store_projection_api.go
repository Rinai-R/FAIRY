package compaction

import (
	"context"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"
)

func (s *Store) CommitPromptProjection(
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	return s.CommitPromptProjectionContext(
		context.Background(), conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		expectedTranscript,
		projection, contextWindow, clearLane,
	)
}

func (s *Store) CommitPromptProjectionContext(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	if !s.usesSeekDB() {
		return Result{}, ErrStoreBackendUnavailable
	}
	return s.commitPromptProjectionSeekDB(
		ctx, conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		expectedTranscript,
		projection, contextWindow, clearLane,
	)
}
