package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) commitTieredCompactionPostgres(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	summary string,
	cutoff uint64,
	projection PromptProjectionState,
	contextWindow ContextWindowRecord,
	clearLane string,
) (CompactionResult, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return CompactionResult{}, err
	}
	value := strings.TrimSpace(summary)
	if value == "" || len([]rune(value)) > 12000 || strings.Contains(value, "\x00") {
		return CompactionResult{}, errors.New("compaction summary is invalid")
	}
	if err := ValidatePromptProjection(projection); err != nil {
		return CompactionResult{}, err
	}
	if err := validateContextWindow(contextWindow); err != nil {
		return CompactionResult{}, err
	}
	if err := validatePromptLane(clearLane); err != nil {
		return CompactionResult{}, err
	}
	expectedWindow, err := databaseInt64("expected window revision", expectedWindowRevision)
	if err != nil {
		return CompactionResult{}, err
	}
	expectedProjection, err := databaseInt64("expected projection revision", expectedProjectionRevision)
	if err != nil {
		return CompactionResult{}, err
	}
	cutoffValue, err := databaseInt64("compaction cutoff", cutoff)
	if err != nil {
		return CompactionResult{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("beginning tiered compaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := validateProjectionAgainstTranscript(queryCtx, tx, conversationID, projection); err != nil {
		return CompactionResult{}, err
	}
	var maxSequence int64
	if err := tx.QueryRow(queryCtx, "SELECT COALESCE(MAX(sequence), 0) FROM conversation_messages WHERE conversation_id = $1", conversationID).Scan(&maxSequence); err != nil {
		return CompactionResult{}, err
	}
	if cutoffValue > maxSequence {
		return CompactionResult{}, errors.New("compaction cutoff exceeds transcript")
	}
	result, err := CommitTieredCompaction(
		queryCtx, tx, conversationID, expectedWindow, expectedProjection,
		value, cutoffValue, projection, contextWindow, clearLane, nowUnixMS(),
	)
	if err != nil {
		return CompactionResult{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return CompactionResult{}, fmt.Errorf("committing tiered compaction: %w", err)
	}
	return result, nil
}
