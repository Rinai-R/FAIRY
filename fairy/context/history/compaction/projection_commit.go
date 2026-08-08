package compaction

import (
	"context"
	"errors"
	"fmt"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"

	"github.com/jackc/pgx/v5"
)

func CommitPromptProjection(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision int64,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
	now int64,
) (Result, error) {
	if expectedWindowRevision < 1 || expectedProjectionRevision < 1 {
		return Result{}, errors.New("expected prompt projection revisions are required")
	}
	nextWindowRevision := expectedWindowRevision + 1
	nextProjectionRevision := expectedProjectionRevision + 1
	if contextWindow.ConversationID != conversationID {
		return Result{}, errors.New("context window conversation does not match projection")
	}
	if contextWindow.PromptWindowRevision != uint64(nextWindowRevision) {
		return Result{}, errors.New("context window revision does not follow projection")
	}
	encoded, err := historyprojection.Encode(projection)
	if err != nil {
		return Result{}, err
	}
	if err := updatePromptProjection(
		ctx, tx, conversationID,
		expectedWindowRevision, expectedProjectionRevision,
		nextWindowRevision, nextProjectionRevision,
		encoded, now,
	); err != nil {
		return Result{}, err
	}
	if err := historyruntime.UpsertContextWindow(ctx, tx, contextWindow, now); err != nil {
		return Result{}, err
	}
	if err := historyruntime.DeleteLaneContinuation(ctx, tx, conversationID, clearLane); err != nil {
		return Result{}, err
	}
	return Result{WindowRevision: uint64(nextWindowRevision)}, nil
}

func validateProjectionAgainstTranscript(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	state historyprojection.State,
) error {
	var maxSequence int64
	if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(sequence), 0)
FROM conversation_messages
WHERE conversation_id = $1`, conversationID).Scan(&maxSequence); err != nil {
		return fmt.Errorf("loading projection transcript boundary: %w", err)
	}
	for index, omission := range state.Omissions {
		if omission.StartMessageSequence == 0 {
			continue
		}
		if omission.EndMessageSequence > uint64(maxSequence) {
			return fmt.Errorf("prompt projection omission %d exceeds transcript", index)
		}
	}
	if state.RecentTailStartSequence > uint64(maxSequence)+1 {
		return errors.New("prompt projection recent tail exceeds transcript")
	}
	return nil
}
