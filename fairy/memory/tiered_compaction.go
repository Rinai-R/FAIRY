package memory

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func CommitTieredCompaction(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision int64,
	summary string,
	cutoff int64,
	projection PromptProjectionState,
	contextWindow ContextWindowRecord,
	clearLane string,
	now int64,
) (CompactionResult, error) {
	if cutoff < 0 {
		return CompactionResult{}, errors.New("compaction cutoff cannot be negative")
	}
	if err := RequireConversation(ctx, tx, conversationID); err != nil {
		return CompactionResult{}, err
	}
	nextWindowRevision := expectedWindowRevision + 1
	nextProjectionRevision := expectedProjectionRevision + 1
	if contextWindow.ConversationID != conversationID ||
		contextWindow.PromptWindowRevision != uint64(nextWindowRevision) {
		return CompactionResult{}, errors.New("context window does not match tiered compaction")
	}
	encoded, err := EncodePromptProjection(projection)
	if err != nil {
		return CompactionResult{}, err
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
		return CompactionResult{}, err
	}
	if changed.RowsAffected() != 1 {
		return CompactionResult{}, ErrPromptWindowRevisionChanged
	}
	if err := UpsertContextWindow(ctx, tx, contextWindow, now); err != nil {
		return CompactionResult{}, err
	}
	if err := DeleteLaneContinuation(ctx, tx, conversationID, clearLane); err != nil {
		return CompactionResult{}, err
	}
	return CompactionResult{WindowRevision: uint64(nextWindowRevision)}, nil
}
