package memory

import (
	"errors"
	"fmt"
	"strings"
)

type CompactionResult struct {
	WindowRevision        uint64 `json:"windowRevision"`
	RetainedDialogueItems int    `json:"retainedDialogueItems"`
}

func (s *Store) CommitPromptWindow(conversationID string, expectedRevision uint64, summary string) (CompactionResult, error) {
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
	db, err := s.openWrite()
	if err != nil {
		return CompactionResult{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return CompactionResult{}, fmt.Errorf("beginning prompt window transaction: %w", err)
	}
	defer tx.Rollback()
	var cutoff int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(sequence), 0) FROM conversation_messages WHERE conversation_id = ?1", conversationID).Scan(&cutoff); err != nil {
		return CompactionResult{}, fmt.Errorf("reading prompt window cutoff: %w", err)
	}
	nextRevision := expectedRevision + 1
	result, err := tx.Exec("UPDATE prompt_windows SET revision = ?3, summary = ?4, cutoff_message_sequence = ?5, updated_at_ms = ?6 WHERE conversation_id = ?1 AND revision = ?2", conversationID, expectedRevision, nextRevision, value, cutoff, now)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("updating prompt window: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return CompactionResult{}, fmt.Errorf("checking prompt window update: %w", err)
	}
	if changed != 1 {
		return CompactionResult{}, errors.New("prompt window revision changed")
	}
	if err := tx.Commit(); err != nil {
		return CompactionResult{}, fmt.Errorf("committing prompt window transaction: %w", err)
	}
	return CompactionResult{WindowRevision: nextRevision, RetainedDialogueItems: 0}, nil
}
