package compaction

import (
	"context"
	"fmt"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"
)

func (s *Store) commitPromptProjectionPostgres(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	if err := transcript.ValidateID("conversation_id", conversationID); err != nil {
		return Result{}, err
	}
	if err := historyprojection.Validate(projection); err != nil {
		return Result{}, err
	}
	if err := historyruntime.ValidateContextWindow(contextWindow); err != nil {
		return Result{}, fmt.Errorf("context window is invalid: %w", err)
	}
	if err := historyruntime.ValidatePromptLane(clearLane); err != nil {
		return Result{}, err
	}
	expectedWindow, err := databaseInt64("expected_window_revision", expectedWindowRevision)
	if err != nil {
		return Result{}, err
	}
	expectedProjection, err := databaseInt64("expected_projection_revision", expectedProjectionRevision)
	if err != nil {
		return Result{}, err
	}
	if _, _, err := nextProjectionRevisions(expectedWindow, expectedProjection); err != nil {
		return Result{}, err
	}
	if _, err := validateTranscriptBoundary(expectedTranscript); err != nil {
		return Result{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Result{}, fmt.Errorf("beginning prompt projection transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	result, err := CommitPromptProjection(
		queryCtx, tx, conversationID, expectedWindow, expectedProjection, expectedTranscript,
		projection, contextWindow, clearLane, nowUnixMS(),
	)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Result{}, fmt.Errorf("committing prompt projection: %w", err)
	}
	return result, nil
}
