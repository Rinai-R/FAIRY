package compaction

import (
	"context"

	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"
)

func (s *Store) CommitCompaction(conversationID string, expectedRevision uint64, expectedTranscript transcript.TranscriptBoundary, summary string, contextWindow historyruntime.ContextWindowRecord, clearLane string) (Result, error) {
	return s.CommitCompactionContext(context.Background(), conversationID, expectedRevision, expectedTranscript, summary, contextWindow, clearLane)
}

func (s *Store) CommitCompactionContext(ctx context.Context, conversationID string, expectedRevision uint64, expectedTranscript transcript.TranscriptBoundary, summary string, contextWindow historyruntime.ContextWindowRecord, clearLane string) (Result, error) {
	if !s.usesSeekDB() {
		return Result{}, ErrStoreBackendUnavailable
	}
	return s.commitCompactionSeekDB(ctx, conversationID, expectedRevision, expectedTranscript, summary, contextWindow, clearLane)
}

func (s *Store) CommitPromptWindow(conversationID string, expectedRevision uint64, expectedTranscript transcript.TranscriptBoundary, summary string) (Result, error) {
	return s.CommitPromptWindowContext(context.Background(), conversationID, expectedRevision, expectedTranscript, summary)
}

func (s *Store) CommitPromptWindowContext(ctx context.Context, conversationID string, expectedRevision uint64, expectedTranscript transcript.TranscriptBoundary, summary string) (Result, error) {
	if !s.usesSeekDB() {
		return Result{}, ErrStoreBackendUnavailable
	}
	return s.commitPromptWindowSeekDB(ctx, conversationID, expectedRevision, expectedTranscript, summary)
}
