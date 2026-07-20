package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *Store) appendTurnRuntimeEventPostgres(ctx context.Context, input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	if err := validateTurnRuntimeEventInput(input); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	metadataJSON, err := normalizeRuntimeMetadataJSON(input.MetadataJSON)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("beginning runtime event transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := requireTurnPostgres(queryCtx, tx, input.ConversationID, input.TurnID); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	sequence, err := nextTurnRuntimeEventSequencePostgres(queryCtx, tx, input.ConversationID, input.TurnID)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	now := nowUnixMS()
	id := newID()
	if _, err := tx.Exec(queryCtx, "INSERT INTO turn_runtime_events(id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)", id, input.ConversationID, input.TurnID, sequence, input.EventType, nullableStringValue(input.State), nullableStringValue(input.Code), metadataJSON, now); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("appending runtime event: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("committing runtime event transaction: %w", err)
	}
	return TurnRuntimeEventRecord{ID: id, ConversationID: input.ConversationID, TurnID: input.TurnID, Sequence: uint64(sequence), EventType: input.EventType, State: cloneStringPtr(input.State), Code: cloneStringPtr(input.Code), MetadataJSON: metadataJSON, CreatedAtUnixMS: now}, nil
}

func (s *Store) listTurnRuntimeEventsPostgres(ctx context.Context, conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return nil, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, "SELECT id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json::text, created_at_ms FROM turn_runtime_events WHERE conversation_id = $1 AND turn_id = $2 ORDER BY sequence ASC", conversationID, turnID)
	if err != nil {
		return nil, fmt.Errorf("listing runtime events: %w", err)
	}
	defer rows.Close()
	records := make([]TurnRuntimeEventRecord, 0)
	for rows.Next() {
		var record TurnRuntimeEventRecord
		var sequence int64
		var state pgtype.Text
		var code pgtype.Text
		if err := rows.Scan(&record.ID, &record.ConversationID, &record.TurnID, &sequence, &record.EventType, &state, &code, &record.MetadataJSON, &record.CreatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning runtime event: %w", err)
		}
		record.MetadataJSON, err = normalizeRuntimeMetadataJSON(record.MetadataJSON)
		if err != nil {
			return nil, fmt.Errorf("normalizing stored runtime metadata: %w", err)
		}
		record.Sequence = uint64(sequence)
		record.State = stringPtrFromPGText(state)
		record.Code = stringPtrFromPGText(code)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating runtime events: %w", err)
	}
	return records, nil
}

func (s *Store) saveLaneContinuationPostgres(ctx context.Context, record LaneContinuationRecord) (LaneContinuationRecord, error) {
	if err := validateLaneContinuation(record); err != nil {
		return LaneContinuationRecord{}, err
	}
	windowRevision, err := databaseInt64("window_revision", record.WindowRevision)
	if err != nil {
		return LaneContinuationRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("beginning lane continuation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := requireConversationPostgres(queryCtx, tx, record.ConversationID); err != nil {
		return LaneContinuationRecord{}, err
	}
	now := nowUnixMS()
	if _, err := tx.Exec(queryCtx, "INSERT INTO lane_continuations(conversation_id, lane, previous_response_id, request_shape_hash, input_prefix_hash, response_item_hash, window_revision, updated_at_ms) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT(conversation_id, lane) DO UPDATE SET previous_response_id = excluded.previous_response_id, request_shape_hash = excluded.request_shape_hash, input_prefix_hash = excluded.input_prefix_hash, response_item_hash = excluded.response_item_hash, window_revision = excluded.window_revision, updated_at_ms = excluded.updated_at_ms", record.ConversationID, record.Lane, record.PreviousResponseID, record.RequestShapeHash, record.InputPrefixHash, record.ResponseItemHash, windowRevision, now); err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("saving lane continuation: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("committing lane continuation transaction: %w", err)
	}
	record.UpdatedAtUnixMS = now
	return record, nil
}

func (s *Store) loadLaneContinuationPostgres(ctx context.Context, conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return LaneContinuationRecord{}, false, err
	}
	if err := validatePromptLane(lane); err != nil {
		return LaneContinuationRecord{}, false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var record LaneContinuationRecord
	var windowRevision int64
	err := s.pool.Raw().QueryRow(queryCtx, "SELECT conversation_id, lane, previous_response_id, request_shape_hash, input_prefix_hash, response_item_hash, window_revision, updated_at_ms FROM lane_continuations WHERE conversation_id = $1 AND lane = $2", conversationID, lane).Scan(&record.ConversationID, &record.Lane, &record.PreviousResponseID, &record.RequestShapeHash, &record.InputPrefixHash, &record.ResponseItemHash, &windowRevision, &record.UpdatedAtUnixMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return LaneContinuationRecord{}, false, nil
	}
	if err != nil {
		return LaneContinuationRecord{}, false, fmt.Errorf("loading lane continuation: %w", err)
	}
	record.WindowRevision = uint64(windowRevision)
	return record, true, nil
}

func (s *Store) clearLaneContinuationPostgres(ctx context.Context, conversationID string, lane string) error {
	if err := validateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := validatePromptLane(lane); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning lane continuation clear transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := requireConversationPostgres(queryCtx, tx, conversationID); err != nil {
		return err
	}
	if _, err := tx.Exec(queryCtx, "DELETE FROM lane_continuations WHERE conversation_id = $1 AND lane = $2", conversationID, lane); err != nil {
		return fmt.Errorf("clearing lane continuation: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing lane continuation clear transaction: %w", err)
	}
	return nil
}

func (s *Store) saveContextWindowPostgres(ctx context.Context, record ContextWindowRecord) (ContextWindowRecord, error) {
	if err := validateContextWindow(record); err != nil {
		return ContextWindowRecord{}, err
	}
	windowNumber, err := databaseInt64("window_number", record.WindowNumber)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	failureCount, err := databaseInt64("failure_count", record.FailureCount)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	promptWindowRevision, err := databaseInt64("prompt_window_revision", record.PromptWindowRevision)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	observedPrefillTokens, err := nullableDatabaseInt64("observed_prefill_tokens", record.ObservedPrefillTokens)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	estimatedPrefillTokens, err := nullableDatabaseInt64("estimated_prefill_tokens", record.EstimatedPrefillTokens)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return ContextWindowRecord{}, fmt.Errorf("beginning context window transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := requireConversationPostgres(queryCtx, tx, record.ConversationID); err != nil {
		return ContextWindowRecord{}, err
	}
	now := nowUnixMS()
	if _, err := tx.Exec(queryCtx, "INSERT INTO context_windows(conversation_id, lane, window_number, first_window_id, previous_window_id, window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger, failure_count, prompt_window_revision, updated_at_ms) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT(conversation_id, lane) DO UPDATE SET window_number = excluded.window_number, first_window_id = excluded.first_window_id, previous_window_id = excluded.previous_window_id, window_id = excluded.window_id, observed_prefill_tokens = excluded.observed_prefill_tokens, estimated_prefill_tokens = excluded.estimated_prefill_tokens, last_trigger = excluded.last_trigger, failure_count = excluded.failure_count, prompt_window_revision = excluded.prompt_window_revision, updated_at_ms = excluded.updated_at_ms", record.ConversationID, record.Lane, windowNumber, record.FirstWindowID, nullableStringValue(record.PreviousWindowID), record.WindowID, observedPrefillTokens, estimatedPrefillTokens, record.LastTrigger, failureCount, promptWindowRevision, now); err != nil {
		return ContextWindowRecord{}, fmt.Errorf("saving context window: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ContextWindowRecord{}, fmt.Errorf("committing context window transaction: %w", err)
	}
	record.UpdatedAtUnixMS = now
	return record, nil
}

func (s *Store) loadContextWindowPostgres(ctx context.Context, conversationID string, lane string) (ContextWindowRecord, bool, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return ContextWindowRecord{}, false, err
	}
	if err := validatePromptLane(lane); err != nil {
		return ContextWindowRecord{}, false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var record ContextWindowRecord
	var windowNumber int64
	var previousWindowID pgtype.Text
	var observedPrefillTokens pgtype.Int8
	var estimatedPrefillTokens pgtype.Int8
	var failureCount int64
	var promptWindowRevision int64
	err := s.pool.Raw().QueryRow(queryCtx, "SELECT conversation_id, lane, window_number, first_window_id, previous_window_id, window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger, failure_count, prompt_window_revision, updated_at_ms FROM context_windows WHERE conversation_id = $1 AND lane = $2", conversationID, lane).Scan(&record.ConversationID, &record.Lane, &windowNumber, &record.FirstWindowID, &previousWindowID, &record.WindowID, &observedPrefillTokens, &estimatedPrefillTokens, &record.LastTrigger, &failureCount, &promptWindowRevision, &record.UpdatedAtUnixMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return ContextWindowRecord{}, false, nil
	}
	if err != nil {
		return ContextWindowRecord{}, false, fmt.Errorf("loading context window: %w", err)
	}
	record.WindowNumber = uint64(windowNumber)
	record.PreviousWindowID = stringPtrFromPGText(previousWindowID)
	record.ObservedPrefillTokens, err = uintPtrFromPGInt8("observed_prefill_tokens", observedPrefillTokens)
	if err != nil {
		return ContextWindowRecord{}, false, err
	}
	record.EstimatedPrefillTokens, err = uintPtrFromPGInt8("estimated_prefill_tokens", estimatedPrefillTokens)
	if err != nil {
		return ContextWindowRecord{}, false, err
	}
	record.FailureCount = uint64(failureCount)
	record.PromptWindowRevision = uint64(promptWindowRevision)
	return record, true, nil
}

func requireTurnPostgres(ctx context.Context, tx pgx.Tx, conversationID string, turnID string) error {
	var exists int
	err := tx.QueryRow(ctx, "SELECT 1 FROM conversation_turns WHERE conversation_id = $1 AND id = $2 FOR UPDATE", conversationID, turnID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("turn does not belong to conversation")
	}
	if err != nil {
		return fmt.Errorf("checking turn: %w", err)
	}
	return nil
}

func nextTurnRuntimeEventSequencePostgres(ctx context.Context, tx pgx.Tx, conversationID string, turnID string) (int64, error) {
	var maxSequence int64
	if err := tx.QueryRow(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM turn_runtime_events WHERE conversation_id = $1 AND turn_id = $2", conversationID, turnID).Scan(&maxSequence); err != nil {
		return 0, fmt.Errorf("reading next runtime event sequence: %w", err)
	}
	return maxSequence + 1, nil
}

func stringPtrFromPGText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func uintPtrFromPGInt8(label string, value pgtype.Int8) (*uint64, error) {
	if !value.Valid {
		return nil, nil
	}
	if value.Int64 < 0 {
		return nil, fmt.Errorf("%s is negative", label)
	}
	result := uint64(value.Int64)
	return &result, nil
}
