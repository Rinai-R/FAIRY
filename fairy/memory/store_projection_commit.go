package memory

import (
	"context"
	"fmt"
)

func (s *Store) commitPromptProjectionPostgres(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	projection PromptProjectionState,
	contextWindow ContextWindowRecord,
	clearLane string,
) (CompactionResult, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return CompactionResult{}, err
	}
	if err := ValidatePromptProjection(projection); err != nil {
		return CompactionResult{}, err
	}
	if err := validateContextWindow(contextWindow); err != nil {
		return CompactionResult{}, fmt.Errorf("context window is invalid: %w", err)
	}
	if err := validatePromptLane(clearLane); err != nil {
		return CompactionResult{}, err
	}
	expectedWindow, err := databaseInt64("expected_window_revision", expectedWindowRevision)
	if err != nil {
		return CompactionResult{}, err
	}
	expectedProjection, err := databaseInt64("expected_projection_revision", expectedProjectionRevision)
	if err != nil {
		return CompactionResult{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("beginning prompt projection transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := validateProjectionAgainstTranscript(queryCtx, tx, conversationID, projection); err != nil {
		return CompactionResult{}, err
	}
	result, err := CommitPromptProjection(
		queryCtx, tx, conversationID, expectedWindow, expectedProjection,
		projection, contextWindow, clearLane, nowUnixMS(),
	)
	if err != nil {
		return CompactionResult{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return CompactionResult{}, fmt.Errorf("committing prompt projection: %w", err)
	}
	return result, nil
}
