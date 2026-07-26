package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func CommitPromptWindow(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedRevision, nextRevision int64,
	summary string,
	now int64,
) (CompactionResult, error) {
	if err := RequireConversation(ctx, tx, conversationID); err != nil {
		return CompactionResult{}, err
	}
	cutoff, err := promptWindowCutoffSequence(ctx, tx, conversationID)
	if err != nil {
		return CompactionResult{}, err
	}
	if err := updatePromptWindow(ctx, tx, conversationID, expectedRevision, nextRevision, summary, cutoff, now); err != nil {
		return CompactionResult{}, err
	}
	return CompactionResult{WindowRevision: uint64(nextRevision), RetainedDialogueItems: 0}, nil
}

func CommitCompaction(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedRevision, nextRevision int64,
	summary string,
	contextWindow ContextWindowRecord,
	clearLane string,
	now int64,
) (CompactionResult, error) {
	if err := RequireConversation(ctx, tx, conversationID); err != nil {
		return CompactionResult{}, err
	}
	cutoff, err := promptWindowCutoffSequence(ctx, tx, conversationID)
	if err != nil {
		return CompactionResult{}, err
	}
	if err := updatePromptWindow(ctx, tx, conversationID, expectedRevision, nextRevision, summary, cutoff, now); err != nil {
		return CompactionResult{}, err
	}
	if err := UpsertContextWindow(ctx, tx, contextWindow, now); err != nil {
		return CompactionResult{}, err
	}
	if err := DeleteLaneContinuation(ctx, tx, conversationID, clearLane); err != nil {
		return CompactionResult{}, err
	}
	return CompactionResult{WindowRevision: uint64(nextRevision)}, nil
}

func promptWindowCutoffSequence(ctx context.Context, tx pgx.Tx, conversationID string) (int64, error) {
	var cutoff int64
	if err := tx.QueryRow(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM conversation_messages WHERE conversation_id = $1", conversationID).Scan(&cutoff); err != nil {
		return 0, fmt.Errorf("reading prompt window cutoff: %w", err)
	}
	return cutoff, nil
}

func updatePromptWindow(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedRevision, nextRevision int64,
	summary string,
	cutoff, now int64,
) error {
	changed, err := tx.Exec(ctx, "UPDATE prompt_windows SET revision = $3, summary = $4, cutoff_message_sequence = $5, updated_at_ms = $6 WHERE conversation_id = $1 AND revision = $2", conversationID, expectedRevision, nextRevision, summary, cutoff, now)
	if err != nil {
		return fmt.Errorf("updating prompt window: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("prompt window revision changed")
	}
	return nil
}
