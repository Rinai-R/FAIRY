package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) commitPromptWindowPostgres(ctx context.Context, conversationID string, expectedRevision uint64, summary string) (CompactionResult, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
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
	if err := requireConversationPostgres(queryCtx, tx, conversationID); err != nil {
		return CompactionResult{}, err
	}
	var cutoff int64
	if err := tx.QueryRow(queryCtx, "SELECT COALESCE(MAX(sequence), 0) FROM conversation_messages WHERE conversation_id = $1", conversationID).Scan(&cutoff); err != nil {
		return CompactionResult{}, fmt.Errorf("reading prompt window cutoff: %w", err)
	}
	changed, err := tx.Exec(queryCtx, "UPDATE prompt_windows SET revision = $3, summary = $4, cutoff_message_sequence = $5, updated_at_ms = $6 WHERE conversation_id = $1 AND revision = $2", conversationID, expected, nextRevision, value, cutoff, nowUnixMS())
	if err != nil {
		return CompactionResult{}, fmt.Errorf("updating prompt window: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return CompactionResult{}, errors.New("prompt window revision changed")
	}
	if err := tx.Commit(queryCtx); err != nil {
		return CompactionResult{}, fmt.Errorf("committing prompt window transaction: %w", err)
	}
	return CompactionResult{WindowRevision: uint64(nextRevision), RetainedDialogueItems: 0}, nil
}
