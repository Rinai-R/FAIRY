package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"
)

func (s *Store) commitTieredCompactionPostgres(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	summary string,
	cutoff uint64,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	if err := transcript.ValidateID("conversation_id", conversationID); err != nil {
		return Result{}, err
	}
	value := strings.TrimSpace(summary)
	if value == "" || len([]rune(value)) > 12000 || strings.Contains(value, "\x00") {
		return Result{}, errors.New("compaction summary is invalid")
	}
	if err := historyprojection.Validate(projection); err != nil {
		return Result{}, err
	}
	if err := historyruntime.ValidateContextWindow(contextWindow); err != nil {
		return Result{}, err
	}
	if err := historyruntime.ValidatePromptLane(clearLane); err != nil {
		return Result{}, err
	}
	expectedWindow, err := databaseInt64("expected window revision", expectedWindowRevision)
	if err != nil {
		return Result{}, err
	}
	expectedProjection, err := databaseInt64("expected projection revision", expectedProjectionRevision)
	if err != nil {
		return Result{}, err
	}
	cutoffValue, err := databaseInt64("compaction cutoff", cutoff)
	if err != nil {
		return Result{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Result{}, fmt.Errorf("beginning tiered compaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := validateProjectionAgainstTranscript(queryCtx, tx, conversationID, projection); err != nil {
		return Result{}, err
	}
	var maxSequence int64
	if err := tx.QueryRow(queryCtx, "SELECT COALESCE(MAX(sequence), 0) FROM conversation_messages WHERE conversation_id = $1", conversationID).Scan(&maxSequence); err != nil {
		return Result{}, err
	}
	if cutoffValue > maxSequence {
		return Result{}, errors.New("compaction cutoff exceeds transcript")
	}
	result, err := CommitTieredCompaction(
		queryCtx, tx, conversationID, expectedWindow, expectedProjection,
		value, cutoffValue, projection, contextWindow, clearLane, nowUnixMS(),
	)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Result{}, fmt.Errorf("committing tiered compaction: %w", err)
	}
	return result, nil
}
