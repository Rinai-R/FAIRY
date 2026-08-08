package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"fairy/context/history/transcript"
)

func (s *Store) commitPromptWindowPostgres(ctx context.Context, conversationID string, expectedRevision uint64, summary string) (Result, error) {
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
	expected, err := databaseInt64("expected prompt window revision", expectedRevision)
	if err != nil {
		return Result{}, err
	}
	nextRevision, err := databaseInt64("next prompt window revision", expectedRevision+1)
	if err != nil {
		return Result{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Result{}, fmt.Errorf("beginning prompt window transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	result, err := CommitPromptWindow(queryCtx, tx, conversationID, expected, nextRevision, value, nowUnixMS())
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Result{}, fmt.Errorf("committing prompt window transaction: %w", err)
	}
	return result, nil
}
