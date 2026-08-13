package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"
)

func (s *Store) commitCompactionPostgres(
	ctx context.Context,
	conversationID string,
	expectedRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	summary string,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	if err := transcript.ValidateID("conversation_id", conversationID); err != nil {
		return Result{}, err
	}
	if expectedRevision == 0 {
		return Result{}, errors.New("expected prompt window revision is required")
	}
	value := strings.TrimSpace(summary)
	if value == "" || len([]rune(value)) > 12000 || strings.Contains(value, "\x00") {
		return Result{}, errors.New("compaction summary is invalid")
	}
	if err := historyruntime.ValidateContextWindow(contextWindow); err != nil {
		return Result{}, fmt.Errorf("context window is invalid: %w", err)
	}
	if contextWindow.ConversationID != conversationID {
		return Result{}, errors.New("context window conversation does not match compaction")
	}
	if contextWindow.PromptWindowRevision != expectedRevision+1 {
		return Result{}, errors.New("context window revision does not follow prompt window")
	}
	if err := historyruntime.ValidatePromptLane(clearLane); err != nil {
		return Result{}, err
	}
	expected, err := databaseInt64("expected prompt window revision", expectedRevision)
	if err != nil {
		return Result{}, err
	}
	nextRevision, err := databaseInt64("next prompt window revision", expectedRevision+1)
	if err != nil {
		return Result{}, err
	}
	if _, err := validateTranscriptBoundary(expectedTranscript); err != nil {
		return Result{}, err
	}

	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Result{}, fmt.Errorf("beginning compaction transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	result, err := CommitCompaction(queryCtx, tx, conversationID, expected, nextRevision, expectedTranscript, value, contextWindow, clearLane, nowUnixMS())
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Result{}, fmt.Errorf("committing compaction transaction: %w", err)
	}
	return result, nil
}
