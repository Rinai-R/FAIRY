package postgres

import (
	"context"
	"errors"
	domainmemory "fairy/internal/domain/memory"
	"fmt"
	"strings"
)

func (s *Store) commitPromptWindowPostgres(ctx context.Context, conversationID string, expectedRevision uint64, summary string) (CompactionResult, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return CompactionResult{}, err
	}
	if expectedRevision == 0 {
		return CompactionResult{}, errors.New("expected prompt window revision is required")
	}
	value := strings.TrimSpace(summary)
	if value == "" || len([]rune(value)) > 12000 || strings.Contains(value, "\x00") {
		return CompactionResult{}, errors.New("compaction summary is invalid")
	}
	expected, err := databaseInt64("expected prompt window revision", expectedRevision)
	if err != nil {
		return CompactionResult{}, err
	}
	nextRevision, err := databaseInt64("next prompt window revision", expectedRevision+1)
	if err != nil {
		return CompactionResult{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("beginning prompt window transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	result, err := CommitPromptWindow(queryCtx, tx, conversationID, expected, nextRevision, value, nowUnixMS())
	if err != nil {
		return CompactionResult{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return CompactionResult{}, fmt.Errorf("committing prompt window transaction: %w", err)
	}
	return result, nil
}
