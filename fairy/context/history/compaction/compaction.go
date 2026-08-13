package compaction

import (
	"context"
	"fmt"

	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"

	"github.com/jackc/pgx/v5"
)

func CommitPromptWindow(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedRevision, nextRevision int64,
	expectedTranscript transcript.TranscriptBoundary,
	summary string,
	now int64,
) (Result, error) {
	boundary, err := validateTranscriptBoundary(expectedTranscript)
	if err != nil {
		return Result{}, err
	}
	if err := transcript.RequireConversation(ctx, tx, conversationID); err != nil {
		return Result{}, err
	}
	if err := requirePostgresTranscriptBoundary(ctx, tx, conversationID, boundary); err != nil {
		return Result{}, err
	}
	if err := updatePromptWindow(ctx, tx, conversationID, expectedRevision, nextRevision, summary, boundary.messageSequence, now); err != nil {
		return Result{}, err
	}
	return Result{WindowRevision: uint64(nextRevision), RetainedDialogueItems: 0}, nil
}

func CommitCompaction(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedRevision, nextRevision int64,
	expectedTranscript transcript.TranscriptBoundary,
	summary string,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
	now int64,
) (Result, error) {
	boundary, err := validateTranscriptBoundary(expectedTranscript)
	if err != nil {
		return Result{}, err
	}
	if err := transcript.RequireConversation(ctx, tx, conversationID); err != nil {
		return Result{}, err
	}
	if err := requirePostgresTranscriptBoundary(ctx, tx, conversationID, boundary); err != nil {
		return Result{}, err
	}
	if err := updatePromptWindow(ctx, tx, conversationID, expectedRevision, nextRevision, summary, boundary.messageSequence, now); err != nil {
		return Result{}, err
	}
	if err := historyruntime.UpsertContextWindow(ctx, tx, contextWindow, now); err != nil {
		return Result{}, err
	}
	if err := historyruntime.DeleteLaneContinuation(ctx, tx, conversationID, clearLane); err != nil {
		return Result{}, err
	}
	return Result{WindowRevision: uint64(nextRevision)}, nil
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
		return ErrPromptWindowRevisionChanged
	}
	return nil
}

func requirePostgresTranscriptBoundary(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expected databaseTranscriptBoundary,
) error {
	var turnSequence, messageSequence int64
	if err := tx.QueryRow(ctx, `
SELECT COALESCE((SELECT MAX(sequence) FROM conversation_turns WHERE conversation_id = $1), 0),
       COALESCE((SELECT MAX(sequence) FROM conversation_messages WHERE conversation_id = $1), 0)`,
		conversationID,
	).Scan(&turnSequence, &messageSequence); err != nil {
		return fmt.Errorf("reading transcript boundary: %w", err)
	}
	if turnSequence != expected.turnSequence || messageSequence != expected.messageSequence {
		return ErrPromptWindowRevisionChanged
	}
	return nil
}
