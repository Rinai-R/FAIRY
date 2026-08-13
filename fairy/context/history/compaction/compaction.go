package compaction

import (
	"context"
	"errors"
	"fmt"

	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"

	"github.com/jackc/pgx/v5"
)

func commitPromptWindow(
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
	if err := requirePostgresConversation(ctx, tx, conversationID); err != nil {
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

func commitCompaction(
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
	if err := requirePostgresConversation(ctx, tx, conversationID); err != nil {
		return Result{}, err
	}
	if err := requirePostgresTranscriptBoundary(ctx, tx, conversationID, boundary); err != nil {
		return Result{}, err
	}
	if err := updatePromptWindow(ctx, tx, conversationID, expectedRevision, nextRevision, summary, boundary.messageSequence, now); err != nil {
		return Result{}, err
	}
	if err := upsertPostgresContextWindow(ctx, tx, contextWindow, now); err != nil {
		return Result{}, err
	}
	if err := deletePostgresLaneContinuation(ctx, tx, conversationID, clearLane); err != nil {
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

func requirePostgresConversation(ctx context.Context, tx pgx.Tx, conversationID string) error {
	var exists int
	err := tx.QueryRow(ctx, "SELECT 1 FROM conversations WHERE id = $1 FOR UPDATE", conversationID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("conversation does not exist")
	}
	if err != nil {
		return fmt.Errorf("checking conversation: %w", err)
	}
	return nil
}

func upsertPostgresContextWindow(
	ctx context.Context,
	tx pgx.Tx,
	record historyruntime.ContextWindowRecord,
	now int64,
) error {
	observedPrefillTokens, err := nullablePostgresInt64("observed_prefill_tokens", record.ObservedPrefillTokens)
	if err != nil {
		return err
	}
	estimatedPrefillTokens, err := nullablePostgresInt64("estimated_prefill_tokens", record.EstimatedPrefillTokens)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO context_windows(
  conversation_id, lane, window_number, first_window_id, previous_window_id,
  window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger,
  failure_count, prompt_window_revision, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT(conversation_id, lane) DO UPDATE SET
  window_number = excluded.window_number,
  first_window_id = excluded.first_window_id,
  previous_window_id = excluded.previous_window_id,
  window_id = excluded.window_id,
  observed_prefill_tokens = excluded.observed_prefill_tokens,
  estimated_prefill_tokens = excluded.estimated_prefill_tokens,
  last_trigger = excluded.last_trigger,
  failure_count = excluded.failure_count,
  prompt_window_revision = excluded.prompt_window_revision,
  updated_at_ms = excluded.updated_at_ms`,
		record.ConversationID, record.Lane, int64(record.WindowNumber), record.FirstWindowID,
		record.PreviousWindowID, record.WindowID, observedPrefillTokens, estimatedPrefillTokens,
		record.LastTrigger, int64(record.FailureCount), int64(record.PromptWindowRevision), now,
	); err != nil {
		return fmt.Errorf("saving context window: %w", err)
	}
	return nil
}

func deletePostgresLaneContinuation(ctx context.Context, tx pgx.Tx, conversationID, lane string) error {
	if _, err := tx.Exec(ctx,
		"DELETE FROM lane_continuations WHERE conversation_id = $1 AND lane = $2",
		conversationID, lane,
	); err != nil {
		return fmt.Errorf("clearing lane continuation: %w", err)
	}
	return nil
}

func nullablePostgresInt64(label string, value *uint64) (any, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := databaseInt64(label, *value)
	if err != nil {
		return nil, err
	}
	return converted, nil
}
