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
	expectedTranscript transcript.TranscriptBoundary,
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
	if _, _, err := nextProjectionRevisions(expectedWindow, expectedProjection); err != nil {
		return Result{}, err
	}
	cutoffValue, err := databaseInt64("compaction cutoff", cutoff)
	if err != nil {
		return Result{}, err
	}
	boundary, err := validateTranscriptBoundary(expectedTranscript)
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
	if cutoffValue > boundary.messageSequence {
		return Result{}, errors.New("compaction cutoff exceeds transcript")
	}
	result, err := CommitTieredCompaction(
		queryCtx, tx, conversationID, expectedWindow, expectedProjection, expectedTranscript,
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
