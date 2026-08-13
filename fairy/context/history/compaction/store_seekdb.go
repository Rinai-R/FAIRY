package compaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	"fairy/context/history/transcript"
)

type seekDBWriteStage string

const (
	seekDBStagePromptAfterCAS         seekDBWriteStage = "prompt.after_cas"
	seekDBStagePromptBeforeCommit     seekDBWriteStage = "prompt.before_commit"
	seekDBStageCompactionAfterPrompt  seekDBWriteStage = "compaction.after_prompt"
	seekDBStageCompactionAfterContext seekDBWriteStage = "compaction.after_context"
	seekDBStageCompactionAfterClear   seekDBWriteStage = "compaction.after_clear"
	seekDBStageCompactionBeforeCommit seekDBWriteStage = "compaction.before_commit"
	seekDBStageProjectionAfterPrompt  seekDBWriteStage = "projection.after_prompt"
	seekDBStageProjectionAfterContext seekDBWriteStage = "projection.after_context"
	seekDBStageProjectionAfterClear   seekDBWriteStage = "projection.after_clear"
	seekDBStageProjectionBeforeCommit seekDBWriteStage = "projection.before_commit"
	seekDBStageTieredAfterPrompt      seekDBWriteStage = "tiered.after_prompt"
	seekDBStageTieredAfterContext     seekDBWriteStage = "tiered.after_context"
	seekDBStageTieredAfterClear       seekDBWriteStage = "tiered.after_clear"
	seekDBStageTieredBeforeCommit     seekDBWriteStage = "tiered.before_commit"
)

func (s *Store) commitPromptWindowSeekDB(
	ctx context.Context,
	conversationID string,
	expectedRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	summary string,
) (Result, error) {
	value, expected, next, err := validateSeekDBPromptWindowCommit(conversationID, expectedRevision, summary)
	if err != nil {
		return Result{}, err
	}
	boundary, err := validateTranscriptBoundary(expectedTranscript)
	if err != nil {
		return Result{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("beginning SeekDB prompt window transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return Result{}, err
	}
	if err := requireSeekDBTranscriptBoundary(queryCtx, tx, conversationID, boundary); err != nil {
		return Result{}, err
	}
	if err := updateSeekDBPromptWindow(queryCtx, tx, conversationID, expected, next, value, boundary.messageSequence, s.currentUnixMS()); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStagePromptAfterCAS); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStagePromptBeforeCommit); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("committing SeekDB prompt window transaction: %w", err)
	}
	return Result{WindowRevision: uint64(next), RetainedDialogueItems: 0}, nil
}

func (s *Store) commitCompactionSeekDB(
	ctx context.Context,
	conversationID string,
	expectedRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	summary string,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	value, expected, next, err := validateSeekDBPromptWindowCommit(conversationID, expectedRevision, summary)
	if err != nil {
		return Result{}, err
	}
	if err := validateSeekDBCompactionWindow(conversationID, uint64(next), contextWindow, clearLane); err != nil {
		return Result{}, err
	}
	boundary, err := validateTranscriptBoundary(expectedTranscript)
	if err != nil {
		return Result{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("beginning SeekDB compaction transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return Result{}, err
	}
	if err := requireSeekDBTranscriptBoundary(queryCtx, tx, conversationID, boundary); err != nil {
		return Result{}, err
	}
	now := s.currentUnixMS()
	if err := updateSeekDBPromptWindow(queryCtx, tx, conversationID, expected, next, value, boundary.messageSequence, now); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageCompactionAfterPrompt); err != nil {
		return Result{}, err
	}
	if err := historyruntime.UpsertContextWindowSeekDB(queryCtx, tx, contextWindow, now); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageCompactionAfterContext); err != nil {
		return Result{}, err
	}
	if err := historyruntime.DeleteLaneContinuationSeekDB(queryCtx, tx, conversationID, clearLane); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageCompactionAfterClear); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageCompactionBeforeCommit); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("committing SeekDB compaction transaction: %w", err)
	}
	return Result{WindowRevision: uint64(next)}, nil
}

func (s *Store) commitPromptProjectionSeekDB(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	expectedWindow, nextWindow, expectedProjection, nextProjection, encoded, err := validateSeekDBProjectionCommit(
		conversationID, expectedWindowRevision, expectedProjectionRevision,
		projection, contextWindow, clearLane,
	)
	if err != nil {
		return Result{}, err
	}
	boundary, err := validateTranscriptBoundary(expectedTranscript)
	if err != nil {
		return Result{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("beginning SeekDB prompt projection transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return Result{}, err
	}
	if err := requireSeekDBTranscriptBoundary(queryCtx, tx, conversationID, boundary); err != nil {
		return Result{}, err
	}
	if err := validateSeekDBProjectionAgainstTranscriptBoundary(projection, boundary.messageSequence); err != nil {
		return Result{}, err
	}
	now := s.currentUnixMS()
	if err := updateSeekDBPromptProjection(
		queryCtx, tx, conversationID,
		expectedWindow, expectedProjection, nextWindow, nextProjection, encoded, now,
	); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageProjectionAfterPrompt); err != nil {
		return Result{}, err
	}
	if err := historyruntime.UpsertContextWindowSeekDB(queryCtx, tx, contextWindow, now); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageProjectionAfterContext); err != nil {
		return Result{}, err
	}
	if err := historyruntime.DeleteLaneContinuationSeekDB(queryCtx, tx, conversationID, clearLane); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageProjectionAfterClear); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageProjectionBeforeCommit); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("committing SeekDB prompt projection transaction: %w", err)
	}
	return Result{WindowRevision: uint64(nextWindow)}, nil
}

func (s *Store) commitTieredCompactionSeekDB(
	ctx context.Context,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	expectedTranscript transcript.TranscriptBoundary,
	summary string,
	cutoff uint64,
	projection historyprojection.State,
	contextWindow historyruntime.ContextWindowRecord,
	clearLane string,
) (Result, error) {
	value := strings.TrimSpace(summary)
	if err := validateSeekDBCompactionSummary(value); err != nil {
		return Result{}, err
	}
	expectedWindow, nextWindow, expectedProjection, nextProjection, encoded, err := validateSeekDBProjectionCommit(
		conversationID, expectedWindowRevision, expectedProjectionRevision,
		projection, contextWindow, clearLane,
	)
	if err != nil {
		return Result{}, err
	}
	boundary, err := validateTranscriptBoundary(expectedTranscript)
	if err != nil {
		return Result{}, err
	}
	cutoffValue, err := databaseInt64("compaction cutoff", cutoff)
	if err != nil {
		return Result{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("beginning SeekDB tiered compaction transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return Result{}, err
	}
	if err := requireSeekDBTranscriptBoundary(queryCtx, tx, conversationID, boundary); err != nil {
		return Result{}, err
	}
	if cutoffValue > boundary.messageSequence {
		return Result{}, errors.New("compaction cutoff exceeds transcript")
	}
	if err := validateSeekDBProjectionAgainstTranscriptBoundary(projection, boundary.messageSequence); err != nil {
		return Result{}, err
	}
	now := s.currentUnixMS()
	result, err := tx.ExecContext(queryCtx, `
UPDATE prompt_windows
SET revision = ?, summary = ?, cutoff_message_sequence = ?,
    projection_revision = ?, projection_state = ?, updated_at_ms = ?
WHERE conversation_id = ? AND revision = ? AND projection_revision = ?`,
		nextWindow, value, cutoffValue, nextProjection, encoded, now,
		conversationID, expectedWindow, expectedProjection,
	)
	if err != nil {
		return Result{}, fmt.Errorf("updating SeekDB tiered compaction: %w", err)
	}
	if err := requireSeekDBCASUpdate(result); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageTieredAfterPrompt); err != nil {
		return Result{}, err
	}
	if err := historyruntime.UpsertContextWindowSeekDB(queryCtx, tx, contextWindow, now); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageTieredAfterContext); err != nil {
		return Result{}, err
	}
	if err := historyruntime.DeleteLaneContinuationSeekDB(queryCtx, tx, conversationID, clearLane); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageTieredAfterClear); err != nil {
		return Result{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageTieredBeforeCommit); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("committing SeekDB tiered compaction transaction: %w", err)
	}
	return Result{WindowRevision: uint64(nextWindow)}, nil
}

func validateSeekDBPromptWindowCommit(conversationID string, expectedRevision uint64, summary string) (string, int64, int64, error) {
	if err := transcript.ValidateID("conversation_id", conversationID); err != nil {
		return "", 0, 0, err
	}
	if expectedRevision == 0 {
		return "", 0, 0, errors.New("expected prompt window revision is required")
	}
	value := strings.TrimSpace(summary)
	if err := validateSeekDBCompactionSummary(value); err != nil {
		return "", 0, 0, err
	}
	expected, err := databaseInt64("expected prompt window revision", expectedRevision)
	if err != nil {
		return "", 0, 0, err
	}
	if expected == math.MaxInt64 {
		return "", 0, 0, errors.New("next prompt window revision exceeds database integer range")
	}
	return value, expected, expected + 1, nil
}

func validateSeekDBCompactionSummary(value string) error {
	if value == "" || len([]rune(value)) > 12000 || strings.Contains(value, "\x00") {
		return errors.New("compaction summary is invalid")
	}
	return nil
}

func validateSeekDBCompactionWindow(conversationID string, nextRevision uint64, window historyruntime.ContextWindowRecord, lane string) error {
	if err := historyruntime.ValidateContextWindow(window); err != nil {
		return fmt.Errorf("context window is invalid: %w", err)
	}
	if window.ConversationID != conversationID {
		return errors.New("context window conversation does not match compaction")
	}
	if window.PromptWindowRevision != nextRevision {
		return errors.New("context window revision does not follow prompt window")
	}
	return historyruntime.ValidatePromptLane(lane)
}

func validateSeekDBProjectionCommit(
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision uint64,
	projection historyprojection.State,
	window historyruntime.ContextWindowRecord,
	lane string,
) (int64, int64, int64, int64, []byte, error) {
	if err := transcript.ValidateID("conversation_id", conversationID); err != nil {
		return 0, 0, 0, 0, nil, err
	}
	if expectedWindowRevision == 0 || expectedProjectionRevision == 0 {
		return 0, 0, 0, 0, nil, errors.New("expected prompt projection revisions are required")
	}
	if err := historyprojection.Validate(projection); err != nil {
		return 0, 0, 0, 0, nil, err
	}
	expectedWindow, err := databaseInt64("expected window revision", expectedWindowRevision)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	expectedProjection, err := databaseInt64("expected projection revision", expectedProjectionRevision)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	nextWindow, nextProjection, err := nextProjectionRevisions(expectedWindow, expectedProjection)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	if err := validateSeekDBCompactionWindow(conversationID, uint64(nextWindow), window, lane); err != nil {
		return 0, 0, 0, 0, nil, err
	}
	encoded, err := historyprojection.Encode(projection)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	return expectedWindow, nextWindow, expectedProjection, nextProjection, encoded, nil
}

func requireSeekDBConversation(ctx context.Context, tx *sql.Tx, conversationID string) error {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM conversations WHERE id = ? FOR UPDATE", conversationID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("conversation does not exist")
	}
	if err != nil {
		return fmt.Errorf("checking SeekDB conversation: %w", err)
	}
	return nil
}

func requireSeekDBTranscriptBoundary(
	ctx context.Context,
	tx *sql.Tx,
	conversationID string,
	expected databaseTranscriptBoundary,
) error {
	var turnSequence, messageSequence int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE((SELECT MAX(sequence) FROM conversation_turns WHERE conversation_id = ?), 0),
       COALESCE((SELECT MAX(sequence) FROM conversation_messages WHERE conversation_id = ?), 0)`,
		conversationID, conversationID,
	).Scan(&turnSequence, &messageSequence); err != nil {
		return fmt.Errorf("reading SeekDB transcript boundary: %w", err)
	}
	if turnSequence < 0 || messageSequence < 0 {
		return errors.New("stored SeekDB transcript boundary is invalid")
	}
	if turnSequence != expected.turnSequence || messageSequence != expected.messageSequence {
		return ErrPromptWindowRevisionChanged
	}
	return nil
}

func updateSeekDBPromptWindow(
	ctx context.Context,
	tx *sql.Tx,
	conversationID string,
	expected, next int64,
	summary string,
	cutoff, now int64,
) error {
	result, err := tx.ExecContext(ctx, `
UPDATE prompt_windows
SET revision = ?, summary = ?, cutoff_message_sequence = ?, updated_at_ms = ?
WHERE conversation_id = ? AND revision = ?`, next, summary, cutoff, now, conversationID, expected)
	if err != nil {
		return fmt.Errorf("updating SeekDB prompt window: %w", err)
	}
	return requireSeekDBCASUpdate(result)
}

func updateSeekDBPromptProjection(
	ctx context.Context,
	tx *sql.Tx,
	conversationID string,
	expectedWindow, expectedProjection, nextWindow, nextProjection int64,
	state []byte,
	now int64,
) error {
	result, err := tx.ExecContext(ctx, `
UPDATE prompt_windows
SET revision = ?, projection_revision = ?, projection_state = ?, updated_at_ms = ?
WHERE conversation_id = ? AND revision = ? AND projection_revision = ?`,
		nextWindow, nextProjection, state, now,
		conversationID, expectedWindow, expectedProjection,
	)
	if err != nil {
		return fmt.Errorf("updating SeekDB prompt projection: %w", err)
	}
	return requireSeekDBCASUpdate(result)
}

func requireSeekDBCASUpdate(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading SeekDB prompt window affected rows: %w", err)
	}
	if rows != 1 {
		return ErrPromptWindowRevisionChanged
	}
	return nil
}

func validateSeekDBProjectionAgainstTranscriptBoundary(state historyprojection.State, maximum int64) error {
	for index, omission := range state.Omissions {
		if omission.StartMessageSequence == 0 {
			continue
		}
		if omission.EndMessageSequence > uint64(maximum) {
			return fmt.Errorf("prompt projection omission %d exceeds transcript", index)
		}
	}
	if state.RecentTailStartSequence > uint64(maximum)+1 {
		return errors.New("prompt projection recent tail exceeds transcript")
	}
	return nil
}

func (s *Store) runSeekDBWriteHook(stage seekDBWriteStage) error {
	if s != nil && s.seekDBWriteHook != nil {
		if err := s.seekDBWriteHook(stage); err != nil {
			return fmt.Errorf("SeekDB compaction transaction interrupted at %s: %w", stage, err)
		}
	}
	return nil
}
