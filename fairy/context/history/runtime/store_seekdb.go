package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

type seekDBWriteStage string

const (
	seekDBStageEventAfterSequence       seekDBWriteStage = "event.after_sequence"
	seekDBStageEventAfterInsert         seekDBWriteStage = "event.after_insert"
	seekDBStageEventBeforeCommit        seekDBWriteStage = "event.before_commit"
	seekDBStageContinuationAfterUpsert  seekDBWriteStage = "continuation.after_upsert"
	seekDBStageContinuationAfterDelete  seekDBWriteStage = "continuation.after_delete"
	seekDBStageContinuationBeforeCommit seekDBWriteStage = "continuation.before_commit"
	seekDBStageContextAfterUpsert       seekDBWriteStage = "context_window.after_upsert"
	seekDBStageContextBeforeCommit      seekDBWriteStage = "context_window.before_commit"
)

func (s *Store) appendTurnRuntimeEventSeekDB(ctx context.Context, input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	if err := validateTurnRuntimeEventInput(input); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	metadataJSON, err := normalizeRuntimeMetadataJSON(input.MetadataJSON)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("beginning SeekDB runtime event transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBTurn(queryCtx, tx, input.ConversationID, input.TurnID); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	sequence, err := nextSeekDBRuntimeEventSequence(queryCtx, tx, input.ConversationID, input.TurnID)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageEventAfterSequence); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	record := TurnRuntimeEventRecord{
		ID:              newID(),
		ConversationID:  input.ConversationID,
		TurnID:          input.TurnID,
		Sequence:        uint64(sequence),
		EventType:       input.EventType,
		State:           cloneStringPtr(input.State),
		Code:            cloneStringPtr(input.Code),
		MetadataJSON:    metadataJSON,
		CreatedAtUnixMS: s.currentUnixMS(),
	}
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO turn_runtime_events(
    id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.ConversationID, record.TurnID, sequence, record.EventType,
		nullableString(record.State), nullableString(record.Code), record.MetadataJSON, record.CreatedAtUnixMS,
	); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("appending SeekDB runtime event: %w", err)
	}
	if err := s.runSeekDBWriteHook(seekDBStageEventAfterInsert); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageEventBeforeCommit); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("committing SeekDB runtime event transaction: %w", err)
	}
	return record, nil
}

func (s *Store) listTurnRuntimeEventsSeekDB(ctx context.Context, conversationID, turnID string) ([]TurnRuntimeEventRecord, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return nil, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var exists int
	if err := s.seekDB.QueryRowContext(queryCtx, `
SELECT 1 FROM conversation_turns WHERE conversation_id = ? AND id = ?`, conversationID, turnID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTurnNotFound
		}
		return nil, fmt.Errorf("checking SeekDB runtime event turn: %w", err)
	}
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT id, conversation_id, turn_id, sequence, event_type, state, code,
       CAST(metadata_json AS CHAR), created_at_ms
FROM turn_runtime_events
WHERE conversation_id = ? AND turn_id = ?
ORDER BY sequence ASC`, conversationID, turnID)
	if err != nil {
		return nil, fmt.Errorf("listing SeekDB runtime events: %w", err)
	}
	defer rows.Close()
	records := make([]TurnRuntimeEventRecord, 0)
	for rows.Next() {
		record, err := scanSeekDBRuntimeEvent(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB runtime events: %w", err)
	}
	return records, nil
}

func (s *Store) saveLaneContinuationSeekDB(ctx context.Context, record LaneContinuationRecord) (LaneContinuationRecord, error) {
	if err := validateLaneContinuation(record); err != nil {
		return LaneContinuationRecord{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("beginning SeekDB lane continuation transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, record.ConversationID); err != nil {
		return LaneContinuationRecord{}, err
	}
	record.UpdatedAtUnixMS = s.currentUnixMS()
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO lane_continuations(
    conversation_id, lane, previous_response_id, request_shape_hash,
    input_prefix_hash, response_item_hash, window_revision, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    previous_response_id = VALUES(previous_response_id),
    request_shape_hash = VALUES(request_shape_hash),
    input_prefix_hash = VALUES(input_prefix_hash),
    response_item_hash = VALUES(response_item_hash),
    window_revision = VALUES(window_revision),
    updated_at_ms = VALUES(updated_at_ms)`,
		record.ConversationID, record.Lane, record.PreviousResponseID, record.RequestShapeHash,
		record.InputPrefixHash, record.ResponseItemHash, record.WindowRevision, record.UpdatedAtUnixMS,
	); err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("saving SeekDB lane continuation: %w", err)
	}
	if err := s.runSeekDBWriteHook(seekDBStageContinuationAfterUpsert); err != nil {
		return LaneContinuationRecord{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageContinuationBeforeCommit); err != nil {
		return LaneContinuationRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("committing SeekDB lane continuation transaction: %w", err)
	}
	return record, nil
}

func (s *Store) loadLaneContinuationSeekDB(ctx context.Context, conversationID, lane string) (LaneContinuationRecord, bool, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return LaneContinuationRecord{}, false, err
	}
	if err := validatePromptLane(lane); err != nil {
		return LaneContinuationRecord{}, false, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var record LaneContinuationRecord
	var revision int64
	err := s.seekDB.QueryRowContext(queryCtx, `
SELECT conversation_id, lane, previous_response_id, request_shape_hash,
       input_prefix_hash, response_item_hash, window_revision, updated_at_ms
FROM lane_continuations WHERE conversation_id = ? AND lane = ?`, conversationID, lane).Scan(
		&record.ConversationID, &record.Lane, &record.PreviousResponseID, &record.RequestShapeHash,
		&record.InputPrefixHash, &record.ResponseItemHash, &revision, &record.UpdatedAtUnixMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return LaneContinuationRecord{}, false, nil
	}
	if err != nil {
		return LaneContinuationRecord{}, false, fmt.Errorf("loading SeekDB lane continuation: %w", err)
	}
	if revision <= 0 || record.UpdatedAtUnixMS < 0 {
		return LaneContinuationRecord{}, false, errors.New("stored SeekDB lane continuation is invalid")
	}
	record.WindowRevision = uint64(revision)
	if err := validateLaneContinuation(record); err != nil {
		return LaneContinuationRecord{}, false, fmt.Errorf("stored SeekDB lane continuation is invalid: %w", err)
	}
	return record, true, nil
}

func (s *Store) clearLaneContinuationSeekDB(ctx context.Context, conversationID, lane string) error {
	if err := validateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := validatePromptLane(lane); err != nil {
		return err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return fmt.Errorf("beginning SeekDB lane continuation clear transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return err
	}
	if err := DeleteLaneContinuationSeekDB(queryCtx, tx, conversationID, lane); err != nil {
		return err
	}
	if err := s.runSeekDBWriteHook(seekDBStageContinuationAfterDelete); err != nil {
		return err
	}
	if err := s.runSeekDBWriteHook(seekDBStageContinuationBeforeCommit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing SeekDB lane continuation clear transaction: %w", err)
	}
	return nil
}

func (s *Store) saveContextWindowSeekDB(ctx context.Context, record ContextWindowRecord) (ContextWindowRecord, error) {
	if err := validateContextWindow(record); err != nil {
		return ContextWindowRecord{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return ContextWindowRecord{}, fmt.Errorf("beginning SeekDB context window transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, record.ConversationID); err != nil {
		return ContextWindowRecord{}, err
	}
	record.UpdatedAtUnixMS = s.currentUnixMS()
	if err := UpsertContextWindowSeekDB(queryCtx, tx, record, record.UpdatedAtUnixMS); err != nil {
		return ContextWindowRecord{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageContextAfterUpsert); err != nil {
		return ContextWindowRecord{}, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageContextBeforeCommit); err != nil {
		return ContextWindowRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContextWindowRecord{}, fmt.Errorf("committing SeekDB context window transaction: %w", err)
	}
	return record, nil
}

func (s *Store) loadContextWindowSeekDB(ctx context.Context, conversationID, lane string) (ContextWindowRecord, bool, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return ContextWindowRecord{}, false, err
	}
	if err := validatePromptLane(lane); err != nil {
		return ContextWindowRecord{}, false, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var record ContextWindowRecord
	var windowNumber, failureCount, promptRevision int64
	var previous sql.NullString
	var observed, estimated sql.NullInt64
	err := s.seekDB.QueryRowContext(queryCtx, `
SELECT conversation_id, lane, window_number, first_window_id, previous_window_id,
       window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger,
       failure_count, prompt_window_revision, updated_at_ms
FROM context_windows WHERE conversation_id = ? AND lane = ?`, conversationID, lane).Scan(
		&record.ConversationID, &record.Lane, &windowNumber, &record.FirstWindowID, &previous,
		&record.WindowID, &observed, &estimated, &record.LastTrigger,
		&failureCount, &promptRevision, &record.UpdatedAtUnixMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextWindowRecord{}, false, nil
	}
	if err != nil {
		return ContextWindowRecord{}, false, fmt.Errorf("loading SeekDB context window: %w", err)
	}
	if windowNumber < 0 || failureCount < 0 || promptRevision <= 0 ||
		(record.UpdatedAtUnixMS < 0) || (observed.Valid && observed.Int64 < 0) ||
		(estimated.Valid && estimated.Int64 < 0) {
		return ContextWindowRecord{}, false, errors.New("stored SeekDB context window is invalid")
	}
	record.WindowNumber = uint64(windowNumber)
	record.FailureCount = uint64(failureCount)
	record.PromptWindowRevision = uint64(promptRevision)
	if previous.Valid {
		record.PreviousWindowID = &previous.String
	}
	if observed.Valid {
		value := uint64(observed.Int64)
		record.ObservedPrefillTokens = &value
	}
	if estimated.Valid {
		value := uint64(estimated.Int64)
		record.EstimatedPrefillTokens = &value
	}
	if err := validateContextWindow(record); err != nil {
		return ContextWindowRecord{}, false, fmt.Errorf("stored SeekDB context window is invalid: %w", err)
	}
	return record, true, nil
}

// UpsertContextWindowSeekDB is the runtime-domain mutation used by the
// compaction Store when prompt, window and continuation must share one tx.
func UpsertContextWindowSeekDB(ctx context.Context, tx *sql.Tx, record ContextWindowRecord, now int64) error {
	if tx == nil {
		return errors.New("SeekDB context window transaction is required")
	}
	if err := validateContextWindow(record); err != nil {
		return err
	}
	observed, err := nullableDatabaseInt64("observed_prefill_tokens", record.ObservedPrefillTokens)
	if err != nil {
		return err
	}
	estimated, err := nullableDatabaseInt64("estimated_prefill_tokens", record.EstimatedPrefillTokens)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO context_windows(
    conversation_id, lane, window_number, first_window_id, previous_window_id,
    window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger,
    failure_count, prompt_window_revision, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    window_number = VALUES(window_number),
    first_window_id = VALUES(first_window_id),
    previous_window_id = VALUES(previous_window_id),
    window_id = VALUES(window_id),
    observed_prefill_tokens = VALUES(observed_prefill_tokens),
    estimated_prefill_tokens = VALUES(estimated_prefill_tokens),
    last_trigger = VALUES(last_trigger),
    failure_count = VALUES(failure_count),
    prompt_window_revision = VALUES(prompt_window_revision),
    updated_at_ms = VALUES(updated_at_ms)`,
		record.ConversationID, record.Lane, record.WindowNumber, record.FirstWindowID,
		nullableString(record.PreviousWindowID), record.WindowID, observed, estimated,
		record.LastTrigger, record.FailureCount, record.PromptWindowRevision, now,
	); err != nil {
		return fmt.Errorf("saving SeekDB context window: %w", err)
	}
	return nil
}

// DeleteLaneContinuationSeekDB participates in cross-domain compaction txs.
func DeleteLaneContinuationSeekDB(ctx context.Context, tx *sql.Tx, conversationID, lane string) error {
	if tx == nil {
		return errors.New("SeekDB lane continuation transaction is required")
	}
	if err := validateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := validatePromptLane(lane); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM lane_continuations WHERE conversation_id = ? AND lane = ?`, conversationID, lane); err != nil {
		return fmt.Errorf("clearing SeekDB lane continuation: %w", err)
	}
	return nil
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

func requireSeekDBTurn(ctx context.Context, tx *sql.Tx, conversationID, turnID string) error {
	var exists int
	err := tx.QueryRowContext(ctx, `
SELECT 1 FROM conversation_turns
WHERE conversation_id = ? AND id = ? FOR UPDATE`, conversationID, turnID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTurnNotFound
	}
	if err != nil {
		return fmt.Errorf("checking SeekDB runtime event turn: %w", err)
	}
	return nil
}

func nextSeekDBRuntimeEventSequence(ctx context.Context, tx *sql.Tx, conversationID, turnID string) (int64, error) {
	var maximum int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(sequence), 0) FROM turn_runtime_events
WHERE conversation_id = ? AND turn_id = ?`, conversationID, turnID).Scan(&maximum); err != nil {
		return 0, fmt.Errorf("reading next SeekDB runtime event sequence: %w", err)
	}
	if maximum == math.MaxInt64 {
		return 0, errors.New("SeekDB runtime event sequence is exhausted")
	}
	return maximum + 1, nil
}

func scanSeekDBRuntimeEvent(row interface{ Scan(...any) error }) (TurnRuntimeEventRecord, error) {
	var record TurnRuntimeEventRecord
	var sequence int64
	var state, code sql.NullString
	if err := row.Scan(
		&record.ID, &record.ConversationID, &record.TurnID, &sequence, &record.EventType,
		&state, &code, &record.MetadataJSON, &record.CreatedAtUnixMS,
	); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("scanning SeekDB runtime event: %w", err)
	}
	if sequence <= 0 || record.CreatedAtUnixMS < 0 {
		return TurnRuntimeEventRecord{}, errors.New("stored SeekDB runtime event is invalid")
	}
	record.Sequence = uint64(sequence)
	if state.Valid {
		record.State = &state.String
	}
	if code.Valid {
		record.Code = &code.String
	}
	if err := validateTurnRuntimeEventInput(TurnRuntimeEventInput{
		ConversationID: record.ConversationID,
		TurnID:         record.TurnID,
		EventType:      record.EventType,
		State:          record.State,
		Code:           record.Code,
		MetadataJSON:   record.MetadataJSON,
	}); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("stored SeekDB runtime event is invalid: %w", err)
	}
	metadata, err := normalizeRuntimeMetadataJSON(record.MetadataJSON)
	if err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("stored SeekDB runtime event is invalid: %w", err)
	}
	record.MetadataJSON = metadata
	return record, nil
}

func (s *Store) runSeekDBWriteHook(stage seekDBWriteStage) error {
	if s != nil && s.seekDBWriteHook != nil {
		if err := s.seekDBWriteHook(stage); err != nil {
			return fmt.Errorf("SeekDB runtime transaction interrupted at %s: %w", stage, err)
		}
	}
	return nil
}
