package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ToolExecutionStatus string

const (
	ToolExecutionPending   ToolExecutionStatus = "pending"
	ToolExecutionCompleted ToolExecutionStatus = "completed"
	ToolExecutionFailed    ToolExecutionStatus = "failed"
	ToolExecutionCancelled ToolExecutionStatus = "cancelled"

	ToolNameDesktopObserve = "desktop_observe"
	MaxToolEvidenceBytes   = 768 << 10
)

type ToolExecutionRecord struct {
	ID                     string              `json:"id"`
	ConversationID         string              `json:"conversationId"`
	TurnID                 string              `json:"turnId"`
	CallID                 string              `json:"callId"`
	ToolName               string              `json:"toolName"`
	Status                 ToolExecutionStatus `json:"status"`
	DeadlineAtUnixMS       int64               `json:"deadlineAtUnixMs"`
	AttemptCount           int                 `json:"attemptCount"`
	LastDispatchedAtUnixMS *int64              `json:"lastDispatchedAtUnixMs,omitempty"`
	ErrorCode              *string             `json:"errorCode,omitempty"`
	ErrorMessage           *string             `json:"errorMessage,omitempty"`
	ResultMediaType        *string             `json:"resultMediaType,omitempty"`
	ResultWidth            *int                `json:"resultWidth,omitempty"`
	ResultHeight           *int                `json:"resultHeight,omitempty"`
	ResultByteCount        *int                `json:"resultByteCount,omitempty"`
	ResultSHA256           *string             `json:"resultSha256,omitempty"`
	CreatedAtUnixMS        int64               `json:"createdAtUnixMs"`
	UpdatedAtUnixMS        int64               `json:"updatedAtUnixMs"`
}

type CreateToolExecutionInput struct {
	ConversationID   string
	TurnID           string
	CallID           string
	ToolName         string
	DeadlineAtUnixMS int64
}

type CompleteToolExecutionInput struct {
	ID              string
	ConversationID  string
	TurnID          string
	CallID          string
	ResultMediaType string
	ResultWidth     int
	ResultHeight    int
	ResultByteCount int
	ResultSHA256    string
}

func validateCreateToolExecution(input CreateToolExecutionInput, now int64) error {
	if err := ValidateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := ValidateID("turn_id", input.TurnID); err != nil {
		return err
	}
	if err := ValidateID("call_id", input.CallID); err != nil {
		return err
	}
	if input.ToolName != ToolNameDesktopObserve {
		return errors.New("tool name is unsupported")
	}
	if input.DeadlineAtUnixMS <= now {
		return errors.New("tool execution deadline must be in the future")
	}
	return nil
}

func validateCompleteToolExecution(input CompleteToolExecutionInput) error {
	for label, value := range map[string]string{
		"execution_id": input.ID, "conversation_id": input.ConversationID,
		"turn_id": input.TurnID, "call_id": input.CallID,
	} {
		if err := ValidateID(label, value); err != nil {
			return err
		}
	}
	if input.ResultMediaType != "image/png" && input.ResultMediaType != "image/jpeg" {
		return errors.New("tool result media type is unsupported")
	}
	if input.ResultWidth <= 0 || input.ResultHeight <= 0 {
		return errors.New("tool result dimensions are invalid")
	}
	if input.ResultByteCount <= 0 || input.ResultByteCount > MaxToolEvidenceBytes {
		return errors.New("tool result byte count is invalid")
	}
	if err := validateHash("result_sha256", input.ResultSHA256); err != nil {
		return err
	}
	return nil
}

func validateToolExecutionFailure(code, message string) error {
	if err := validateRuntimeToken("tool execution error code", code); err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" || strings.TrimSpace(message) != message || ContainsDisallowedControl(message) || len([]rune(message)) > 512 {
		return errors.New("tool execution error message is invalid")
	}
	return nil
}

func (s *Store) createToolExecutionPostgres(ctx context.Context, input CreateToolExecutionInput) (ToolExecutionRecord, error) {
	now := nowUnixMS()
	if err := validateCreateToolExecution(input, now); err != nil {
		return ToolExecutionRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return ToolExecutionRecord{}, fmt.Errorf("beginning tool execution transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := RequireTurn(queryCtx, tx, input.ConversationID, input.TurnID); err != nil {
		return ToolExecutionRecord{}, err
	}
	var turnStatus string
	if err := tx.QueryRow(queryCtx, "SELECT status FROM conversation_turns WHERE id = $1 AND conversation_id = $2", input.TurnID, input.ConversationID).Scan(&turnStatus); err != nil {
		return ToolExecutionRecord{}, fmt.Errorf("reading tool execution turn status: %w", err)
	}
	if turnStatus != "interpreting" && turnStatus != "planning" {
		return ToolExecutionRecord{}, errors.New("tool execution requires an active turn")
	}
	record := ToolExecutionRecord{
		ID: newID(), ConversationID: input.ConversationID, TurnID: input.TurnID,
		CallID: input.CallID, ToolName: input.ToolName, Status: ToolExecutionPending,
		DeadlineAtUnixMS: input.DeadlineAtUnixMS, CreatedAtUnixMS: now, UpdatedAtUnixMS: now,
	}
	if _, err := tx.Exec(queryCtx, `
INSERT INTO tool_executions(
    id, conversation_id, turn_id, call_id, tool_name, status,
    deadline_at_ms, attempt_count, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, 'pending', $6, 0, $7, $7)`,
		record.ID, record.ConversationID, record.TurnID, record.CallID,
		record.ToolName, record.DeadlineAtUnixMS, now,
	); err != nil {
		return ToolExecutionRecord{}, fmt.Errorf("creating tool execution: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ToolExecutionRecord{}, fmt.Errorf("committing tool execution: %w", err)
	}
	return record, nil
}

func (s *Store) markToolExecutionDispatchedPostgres(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	if err := ValidateID("execution_id", id); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	now := nowUnixMS()
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	row := s.pool.Raw().QueryRow(queryCtx, `
UPDATE tool_executions
SET attempt_count = attempt_count + 1, last_dispatched_at_ms = $2, updated_at_ms = $2
WHERE id = $1 AND status = 'pending' AND deadline_at_ms > $2
RETURNING `+toolExecutionColumns, id, now)
	record, err := scanToolExecution(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolExecutionRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *Store) completeToolExecutionPostgres(ctx context.Context, input CompleteToolExecutionInput) (ToolExecutionRecord, bool, error) {
	if err := validateCompleteToolExecution(input); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	now := nowUnixMS()
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	row := s.pool.Raw().QueryRow(queryCtx, `
UPDATE tool_executions
SET status = 'completed', result_media_type = $6, result_width = $7,
    result_height = $8, result_byte_count = $9, result_sha256 = $10, updated_at_ms = $11
WHERE id = $1 AND conversation_id = $2 AND turn_id = $3 AND call_id = $4
  AND tool_name = $5 AND status = 'pending' AND deadline_at_ms > $11
RETURNING `+toolExecutionColumns,
		input.ID, input.ConversationID, input.TurnID, input.CallID, ToolNameDesktopObserve,
		input.ResultMediaType, input.ResultWidth, input.ResultHeight,
		input.ResultByteCount, input.ResultSHA256, now,
	)
	record, err := scanToolExecution(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolExecutionRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *Store) failToolExecutionPostgres(ctx context.Context, id, code, message string) (ToolExecutionRecord, bool, error) {
	if err := ValidateID("execution_id", id); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	if err := validateToolExecutionFailure(code, message); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	now := nowUnixMS()
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	row := s.pool.Raw().QueryRow(queryCtx, `
UPDATE tool_executions
SET status = 'failed', error_code = $2, error_message = $3, updated_at_ms = $4
WHERE id = $1 AND status = 'pending'
RETURNING `+toolExecutionColumns, id, code, message, now)
	record, err := scanToolExecution(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolExecutionRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *Store) cancelToolExecutionsForTurnPostgres(ctx context.Context, conversationID, turnID, code, message string) (int64, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return 0, err
	}
	if err := ValidateID("turn_id", turnID); err != nil {
		return 0, err
	}
	if err := validateToolExecutionFailure(code, message); err != nil {
		return 0, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	result, err := s.pool.Raw().Exec(queryCtx, `
UPDATE tool_executions
SET status = 'cancelled', error_code = $3, error_message = $4, updated_at_ms = $5
WHERE conversation_id = $1 AND turn_id = $2 AND status = 'pending'`,
		conversationID, turnID, code, message, nowUnixMS(),
	)
	if err != nil {
		return 0, fmt.Errorf("cancelling tool executions: %w", err)
	}
	return result.RowsAffected(), nil
}

func (s *Store) expireToolExecutionsPostgres(ctx context.Context, now int64) (int64, error) {
	if now < 0 {
		return 0, errors.New("expiration time is invalid")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	result, err := s.pool.Raw().Exec(queryCtx, `
UPDATE tool_executions
SET status = 'failed', error_code = 'deadline_exceeded',
    error_message = 'desktop capture deadline exceeded', updated_at_ms = $1
WHERE status = 'pending' AND deadline_at_ms <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("expiring tool executions: %w", err)
	}
	return result.RowsAffected(), nil
}

func (s *Store) loadToolExecutionPostgres(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	if err := ValidateID("execution_id", id); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	record, err := scanToolExecution(s.pool.Raw().QueryRow(queryCtx, "SELECT "+toolExecutionColumns+" FROM tool_executions WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolExecutionRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *Store) listPendingToolExecutionsPostgres(ctx context.Context, now int64) ([]ToolExecutionRecord, error) {
	if now < 0 {
		return nil, errors.New("pending lookup time is invalid")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, "SELECT "+toolExecutionColumns+" FROM tool_executions WHERE status = 'pending' AND deadline_at_ms > $1 ORDER BY created_at_ms ASC, id ASC", now)
	if err != nil {
		return nil, fmt.Errorf("listing pending tool executions: %w", err)
	}
	defer rows.Close()
	records := make([]ToolExecutionRecord, 0)
	for rows.Next() {
		record, err := scanToolExecution(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pending tool executions: %w", err)
	}
	return records, nil
}

func (s *Store) listRecoverableToolExecutionsPostgres(ctx context.Context) ([]ToolExecutionRecord, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT `+prefixedToolExecutionColumns("execution")+`
FROM tool_executions execution
JOIN conversation_turns turn ON turn.id = execution.turn_id AND turn.conversation_id = execution.conversation_id
WHERE execution.tool_name = $1
  AND execution.status IN ('pending', 'completed')
  AND turn.status IN ('interpreting', 'planning')
ORDER BY execution.created_at_ms ASC, execution.id ASC`, ToolNameDesktopObserve)
	if err != nil {
		return nil, fmt.Errorf("listing recoverable tool executions: %w", err)
	}
	defer rows.Close()
	records := make([]ToolExecutionRecord, 0)
	for rows.Next() {
		record, err := scanToolExecution(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating recoverable tool executions: %w", err)
	}
	return records, nil
}

const toolExecutionColumns = `id, conversation_id, turn_id, call_id, tool_name, status,
deadline_at_ms, attempt_count, last_dispatched_at_ms, error_code, error_message,
result_media_type, result_width, result_height, result_byte_count, result_sha256,
created_at_ms, updated_at_ms`

func prefixedToolExecutionColumns(prefix string) string {
	columns := strings.Split(toolExecutionColumns, ",")
	for index := range columns {
		columns[index] = prefix + "." + strings.TrimSpace(columns[index])
	}
	return strings.Join(columns, ", ")
}

func scanToolExecution(row scanner) (ToolExecutionRecord, error) {
	var record ToolExecutionRecord
	var status string
	var dispatched pgtype.Int8
	var errorCode, errorMessage, mediaType, hash pgtype.Text
	var width, height, byteCount pgtype.Int4
	if err := row.Scan(
		&record.ID, &record.ConversationID, &record.TurnID, &record.CallID, &record.ToolName, &status,
		&record.DeadlineAtUnixMS, &record.AttemptCount, &dispatched, &errorCode, &errorMessage,
		&mediaType, &width, &height, &byteCount, &hash, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS,
	); err != nil {
		return ToolExecutionRecord{}, err
	}
	record.Status = ToolExecutionStatus(status)
	record.LastDispatchedAtUnixMS = int64PtrFromPG(dispatched)
	record.ErrorCode = stringPtrFromPGText(errorCode)
	record.ErrorMessage = stringPtrFromPGText(errorMessage)
	record.ResultMediaType = stringPtrFromPGText(mediaType)
	record.ResultWidth = intPtrFromPG(width)
	record.ResultHeight = intPtrFromPG(height)
	record.ResultByteCount = intPtrFromPG(byteCount)
	record.ResultSHA256 = stringPtrFromPGText(hash)
	return record, nil
}

func int64PtrFromPG(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func intPtrFromPG(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int32)
	return &result
}
