package compaction

import (
	"context"
	"errors"
	"fmt"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"

	"github.com/jackc/pgx/v5"
)

func CommitPromptProjection(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision int64,
	expectedTranscript transcript.TranscriptBoundary,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
	now int64,
) (Result, error) {
	nextWindowRevision, nextProjectionRevision, err := nextProjectionRevisions(
		expectedWindowRevision,
		expectedProjectionRevision,
	)
	if err != nil {
		return Result{}, err
	}
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
	if err := validateProjectionAgainstTranscriptBoundary(projection, boundary.messageSequence); err != nil {
		return Result{}, err
	}
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

func validateProjectionAgainstTranscriptBoundary(state historyprojection.State, maxSequence int64) error {
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
