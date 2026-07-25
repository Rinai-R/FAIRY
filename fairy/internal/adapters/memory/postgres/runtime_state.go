package postgres

import (
	"context"
	"errors"
	"fmt"

	domainmemory "fairy/internal/domain/memory"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func RequireTurn(ctx context.Context, tx pgx.Tx, conversationID, turnID string) error {
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

func NextTurnRuntimeEventSequence(ctx context.Context, tx pgx.Tx, conversationID, turnID string) (int64, error) {
	var maxSequence int64
	if err := tx.QueryRow(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM turn_runtime_events WHERE conversation_id = $1 AND turn_id = $2", conversationID, turnID).Scan(&maxSequence); err != nil {
		return 0, fmt.Errorf("reading next runtime event sequence: %w", err)
	}
	return maxSequence + 1, nil
}

func InsertTurnRuntimeEvent(
	ctx context.Context,
	tx pgx.Tx,
	id, metadataJSON string,
	now int64,
	input domainmemory.TurnRuntimeEventInput,
	sequence int64,
) (domainmemory.TurnRuntimeEventRecord, error) {
	state := nullableString(input.State)
	code := nullableString(input.Code)
	if _, err := tx.Exec(ctx, "INSERT INTO turn_runtime_events(id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)", id, input.ConversationID, input.TurnID, sequence, input.EventType, state, code, metadataJSON, now); err != nil {
		return domainmemory.TurnRuntimeEventRecord{}, fmt.Errorf("appending runtime event: %w", err)
	}
	return domainmemory.TurnRuntimeEventRecord{
		ID:              id,
		ConversationID:  input.ConversationID,
		TurnID:          input.TurnID,
		Sequence:        uint64(sequence),
		EventType:       input.EventType,
		State:           cloneStringPtr(input.State),
		Code:            cloneStringPtr(input.Code),
		MetadataJSON:    metadataJSON,
		CreatedAtUnixMS: now,
	}, nil
}

func ListTurnRuntimeEvents(ctx context.Context, db Querier, conversationID, turnID string) ([]domainmemory.TurnRuntimeEventRecord, error) {
	rows, err := db.Query(ctx, "SELECT id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json::text, created_at_ms FROM turn_runtime_events WHERE conversation_id = $1 AND turn_id = $2 ORDER BY sequence ASC", conversationID, turnID)
	if err != nil {
		return nil, fmt.Errorf("listing runtime events: %w", err)
	}
	defer rows.Close()
	records := make([]domainmemory.TurnRuntimeEventRecord, 0)
	for rows.Next() {
		record, err := scanTurnRuntimeEvent(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating runtime events: %w", err)
	}
	return records, nil
}

func scanTurnRuntimeEvent(row scanner) (domainmemory.TurnRuntimeEventRecord, error) {
	var record domainmemory.TurnRuntimeEventRecord
	var sequence int64
	var state pgtype.Text
	var code pgtype.Text
	if err := row.Scan(&record.ID, &record.ConversationID, &record.TurnID, &sequence, &record.EventType, &state, &code, &record.MetadataJSON, &record.CreatedAtUnixMS); err != nil {
		return domainmemory.TurnRuntimeEventRecord{}, fmt.Errorf("scanning runtime event: %w", err)
	}
	record.Sequence = uint64(sequence)
	record.State = stringPtrFromPGText(state)
	record.Code = stringPtrFromPGText(code)
	return record, nil
}

func SaveLaneContinuation(ctx context.Context, tx pgx.Tx, record domainmemory.LaneContinuationRecord, now int64) (domainmemory.LaneContinuationRecord, error) {
	windowRevision := int64(record.WindowRevision)
	if _, err := tx.Exec(ctx, "INSERT INTO lane_continuations(conversation_id, lane, previous_response_id, request_shape_hash, input_prefix_hash, response_item_hash, window_revision, updated_at_ms) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT(conversation_id, lane) DO UPDATE SET previous_response_id = excluded.previous_response_id, request_shape_hash = excluded.request_shape_hash, input_prefix_hash = excluded.input_prefix_hash, response_item_hash = excluded.response_item_hash, window_revision = excluded.window_revision, updated_at_ms = excluded.updated_at_ms", record.ConversationID, record.Lane, record.PreviousResponseID, record.RequestShapeHash, record.InputPrefixHash, record.ResponseItemHash, windowRevision, now); err != nil {
		return domainmemory.LaneContinuationRecord{}, fmt.Errorf("saving lane continuation: %w", err)
	}
	record.UpdatedAtUnixMS = now
	return record, nil
}

func LoadLaneContinuation(ctx context.Context, db RowQuerier, conversationID, lane string) (domainmemory.LaneContinuationRecord, bool, error) {
	var record domainmemory.LaneContinuationRecord
	var windowRevision int64
	err := db.QueryRow(ctx, "SELECT conversation_id, lane, previous_response_id, request_shape_hash, input_prefix_hash, response_item_hash, window_revision, updated_at_ms FROM lane_continuations WHERE conversation_id = $1 AND lane = $2", conversationID, lane).Scan(&record.ConversationID, &record.Lane, &record.PreviousResponseID, &record.RequestShapeHash, &record.InputPrefixHash, &record.ResponseItemHash, &windowRevision, &record.UpdatedAtUnixMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return domainmemory.LaneContinuationRecord{}, false, nil
	}
	if err != nil {
		return domainmemory.LaneContinuationRecord{}, false, fmt.Errorf("loading lane continuation: %w", err)
	}
	record.WindowRevision = uint64(windowRevision)
	return record, true, nil
}

func DeleteLaneContinuation(ctx context.Context, tx pgx.Tx, conversationID, lane string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM lane_continuations WHERE conversation_id = $1 AND lane = $2", conversationID, lane); err != nil {
		return fmt.Errorf("clearing lane continuation: %w", err)
	}
	return nil
}

func UpsertContextWindow(ctx context.Context, tx pgx.Tx, record domainmemory.ContextWindowRecord, now int64) error {
	windowNumber := int64(record.WindowNumber)
	failureCount := int64(record.FailureCount)
	promptWindowRevision := int64(record.PromptWindowRevision)
	observedPrefillTokens, err := nullableInt64FromUintPtr("observed_prefill_tokens", record.ObservedPrefillTokens)
	if err != nil {
		return err
	}
	estimatedPrefillTokens, err := nullableInt64FromUintPtr("estimated_prefill_tokens", record.EstimatedPrefillTokens)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO context_windows(conversation_id, lane, window_number, first_window_id, previous_window_id, window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger, failure_count, prompt_window_revision, updated_at_ms) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT(conversation_id, lane) DO UPDATE SET window_number = excluded.window_number, first_window_id = excluded.first_window_id, previous_window_id = excluded.previous_window_id, window_id = excluded.window_id, observed_prefill_tokens = excluded.observed_prefill_tokens, estimated_prefill_tokens = excluded.estimated_prefill_tokens, last_trigger = excluded.last_trigger, failure_count = excluded.failure_count, prompt_window_revision = excluded.prompt_window_revision, updated_at_ms = excluded.updated_at_ms", record.ConversationID, record.Lane, windowNumber, record.FirstWindowID, record.PreviousWindowID, record.WindowID, observedPrefillTokens, estimatedPrefillTokens, record.LastTrigger, failureCount, promptWindowRevision, now); err != nil {
		return fmt.Errorf("saving context window: %w", err)
	}
	return nil
}

func SaveContextWindow(ctx context.Context, tx pgx.Tx, record domainmemory.ContextWindowRecord, now int64) (domainmemory.ContextWindowRecord, error) {
	if err := RequireConversation(ctx, tx, record.ConversationID); err != nil {
		return domainmemory.ContextWindowRecord{}, err
	}
	if err := UpsertContextWindow(ctx, tx, record, now); err != nil {
		return domainmemory.ContextWindowRecord{}, err
	}
	record.UpdatedAtUnixMS = now
	return record, nil
}

func LoadContextWindow(ctx context.Context, db RowQuerier, conversationID, lane string) (domainmemory.ContextWindowRecord, bool, error) {
	var record domainmemory.ContextWindowRecord
	var windowNumber int64
	var previousWindowID pgtype.Text
	var observedPrefillTokens pgtype.Int8
	var estimatedPrefillTokens pgtype.Int8
	var failureCount int64
	var promptWindowRevision int64
	err := db.QueryRow(ctx, "SELECT conversation_id, lane, window_number, first_window_id, previous_window_id, window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger, failure_count, prompt_window_revision, updated_at_ms FROM context_windows WHERE conversation_id = $1 AND lane = $2", conversationID, lane).Scan(&record.ConversationID, &record.Lane, &windowNumber, &record.FirstWindowID, &previousWindowID, &record.WindowID, &observedPrefillTokens, &estimatedPrefillTokens, &record.LastTrigger, &failureCount, &promptWindowRevision, &record.UpdatedAtUnixMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return domainmemory.ContextWindowRecord{}, false, nil
	}
	if err != nil {
		return domainmemory.ContextWindowRecord{}, false, fmt.Errorf("loading context window: %w", err)
	}
	record.WindowNumber = uint64(windowNumber)
	record.PreviousWindowID = stringPtrFromPGText(previousWindowID)
	record.ObservedPrefillTokens, err = uintPtrFromPGInt8("observed_prefill_tokens", observedPrefillTokens)
	if err != nil {
		return domainmemory.ContextWindowRecord{}, false, err
	}
	record.EstimatedPrefillTokens, err = uintPtrFromPGInt8("estimated_prefill_tokens", estimatedPrefillTokens)
	if err != nil {
		return domainmemory.ContextWindowRecord{}, false, err
	}
	record.FailureCount = uint64(failureCount)
	record.PromptWindowRevision = uint64(promptWindowRevision)
	return record, true, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
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

func nullableInt64FromUintPtr(label string, value *uint64) (any, error) {
	if value == nil {
		return nil, nil
	}
	if *value > uint64(1<<63-1) {
		return nil, fmt.Errorf("%s exceeds database integer range", label)
	}
	return int64(*value), nil
}
