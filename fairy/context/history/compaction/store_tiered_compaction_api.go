package compaction

import (
	"context"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"
)

func (s *Store) CommitTieredCompaction(
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	summary string,
	cutoff uint64,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	return s.CommitTieredCompactionContext(
		context.Background(), conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		expectedTranscript,
		summary, cutoff, projection, contextWindow, clearLane,
	)
}

func (s *Store) CommitTieredCompactionContext(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	summary string,
	cutoff uint64,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	if !s.usesSeekDB() {
		return Result{}, ErrStoreBackendUnavailable
	}
	return s.commitTieredCompactionSeekDB(
			ctx, conversationID,
			expectedWindowRevision, expectedProjectionRevision,
			expectedTranscript,
			summary, cutoff, projection, contextWindow, clearLane,
		)
}
