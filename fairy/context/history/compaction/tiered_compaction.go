package compaction

import (
	"context"
	"errors"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"

	"github.com/jackc/pgx/v5"
)

func CommitTieredCompaction(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision int64,
	summary string,
	cutoff int64,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
	now int64,
) (Result, error) {
	if cutoff < 0 {
		return Result{}, errors.New("compaction cutoff cannot be negative")
	}
	if err := transcript.RequireConversation(ctx, tx, conversationID); err != nil {
		return Result{}, err
	}
	nextWindowRevision := expectedWindowRevision + 1
	nextProjectionRevision := expectedProjectionRevision + 1
	if contextWindow.ConversationID != conversationID ||
		contextWindow.PromptWindowRevision != uint64(nextWindowRevision) {
		return Result{}, errors.New("context window does not match tiered compaction")
	}
	encoded, err := historyprojection.Encode(projection)
	if err != nil {
		return Result{}, err
	}
	changed, err := tx.Exec(ctx, `
UPDATE prompt_windows
SET revision = $4,
    summary = $5,
    cutoff_message_sequence = $6,
    projection_revision = $7,
    projection_state = $8,
    updated_at_ms = $9
WHERE conversation_id = $1
  AND revision = $2
  AND projection_revision = $3`,
		conversationID, expectedWindowRevision, expectedProjectionRevision,
		nextWindowRevision, summary, cutoff, nextProjectionRevision, encoded, now)
	if err != nil {
		return Result{}, err
	}
	if changed.RowsAffected() != 1 {
		return Result{}, ErrPromptWindowRevisionChanged
	}
	if err := historyruntime.UpsertContextWindow(ctx, tx, contextWindow, now); err != nil {
		return Result{}, err
	}
	if err := historyruntime.DeleteLaneContinuation(ctx, tx, conversationID, clearLane); err != nil {
		return Result{}, err
	}
	return Result{WindowRevision: uint64(nextWindowRevision)}, nil
}
