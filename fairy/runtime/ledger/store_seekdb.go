package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const seekDBToolExecutionColumns = `id, conversation_id, turn_id, call_id, tool_name, status,
deadline_at_ms, attempt_count, last_dispatched_at_ms, error_code, error_message,
result_media_type, result_width, result_height, result_byte_count, result_sha256,
created_at_ms, updated_at_ms`

const seekDBUsageLedgerStreamQuery = `
SELECT event.conversation_id,
       event.turn_id,
       conversation.character_id,
       event.event_type,
       event.state,
       CAST(event.metadata_json AS CHAR),
       event.created_at_ms
FROM turn_runtime_events AS event
LEFT JOIN conversations AS conversation ON conversation.id = event.conversation_id
WHERE event.event_type IN ('model', 'terminal')
ORDER BY event.conversation_id ASC, event.turn_id ASC, event.sequence ASC`

func (s *Store) aggregateTokenUsageSeekDB(ctx context.Context, limit int) (UsageReport, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, seekDBUsageLedgerStreamQuery)
	if err != nil {
		return UsageReport{}, fmt.Errorf("querying runtime usage events: %w", err)
	}
	defer rows.Close()

	collector := newUsageReportCollector(limit)
	var current *usageTurnAccumulator
	currentKey := ""
	currentCharacterID := ""
	for rows.Next() {
		var row usageLedgerRow
		var state sql.NullString
		var characterID sql.NullString
		if err := rows.Scan(
			&row.conversationID,
			&row.turnID,
			&characterID,
			&row.eventType,
			&state,
			&row.metadataJSON,
			&row.createdAtUnixMS,
		); err != nil {
			return UsageReport{}, fmt.Errorf("scanning runtime usage event: %w", err)
		}
		if state.Valid {
			value := state.String
			row.state = &value
		}
		key := row.conversationID + "\x00" + row.turnID
		if currentKey != key {
			collector.Add(current, currentCharacterID)
			current = &usageTurnAccumulator{
				conversationID: row.conversationID,
				turnID:         row.turnID,
				status:         usageTurnStatusUnknown,
				lanes:          make(map[string]*UsageLaneAggregate),
			}
			currentKey = key
			currentCharacterID = ""
			if characterID.Valid {
				currentCharacterID = characterID.String
			}
		}
		if err := applyUsageLedgerRow(current, row); err != nil {
			return UsageReport{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return UsageReport{}, fmt.Errorf("iterating runtime usage events: %w", err)
	}
	collector.Add(current, currentCharacterID)
	return collector.Report(), nil
}

func (s *Store) createToolExecutionSeekDB(ctx context.Context, input CreateToolExecutionInput) (ToolExecutionRecord, error) {
	now := s.currentUnixMS()
	if err := validateCreateToolExecution(input, now); err != nil {
		return ToolExecutionRecord{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return ToolExecutionRecord{}, fmt.Errorf("beginning tool execution transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireSeekDBToolTurn(queryCtx, tx, input.ConversationID, input.TurnID); err != nil {
		return ToolExecutionRecord{}, err
	}
	record := ToolExecutionRecord{
		ID: newID(), ConversationID: input.ConversationID, TurnID: input.TurnID,
		CallID: input.CallID, ToolName: input.ToolName, Status: ToolExecutionPending,
		DeadlineAtUnixMS: input.DeadlineAtUnixMS, CreatedAtUnixMS: now, UpdatedAtUnixMS: now,
	}
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO tool_executions(
  id, conversation_id, turn_id, call_id, tool_name, status,
  deadline_at_ms, attempt_count, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, 'pending', ?, 0, ?, ?)`,
		record.ID, record.ConversationID, record.TurnID, record.CallID,
		record.ToolName, record.DeadlineAtUnixMS, now, now,
	); err != nil {
		return ToolExecutionRecord{}, fmt.Errorf("creating tool execution: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ToolExecutionRecord{}, fmt.Errorf("committing tool execution: %w", err)
	}
	return record, nil
}

func (s *Store) markToolExecutionDispatchedSeekDB(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	if err := validateID("execution_id", id); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	now := s.currentUnixMS()
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	result, err := s.seekDB.ExecContext(queryCtx, `
UPDATE tool_executions
SET attempt_count = attempt_count + 1, last_dispatched_at_ms = ?, updated_at_ms = ?
WHERE id = ? AND status = 'pending' AND deadline_at_ms > ?`, now, now, id, now)
	if err != nil {
		return ToolExecutionRecord{}, false, fmt.Errorf("marking tool execution dispatched: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ToolExecutionRecord{}, false, fmt.Errorf("marking tool execution dispatched: %w", err)
	}
	if changed == 0 {
		return ToolExecutionRecord{}, false, nil
	}
	record, err := s.loadChangedSeekDBToolExecution(ctx, id)
	if err != nil {
		return ToolExecutionRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) completeToolExecutionSeekDB(ctx context.Context, input CompleteToolExecutionInput) (ToolExecutionRecord, bool, error) {
	if err := validateCompleteToolExecution(input); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	now := s.currentUnixMS()
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	result, err := s.seekDB.ExecContext(queryCtx, `
UPDATE tool_executions
SET status = 'completed', result_media_type = ?, result_width = ?,
    result_height = ?, result_byte_count = ?, result_sha256 = ?, updated_at_ms = ?
WHERE id = ? AND conversation_id = ? AND turn_id = ? AND call_id = ?
  AND tool_name = ? AND status = 'pending' AND deadline_at_ms > ?`,
		input.ResultMediaType, input.ResultWidth, input.ResultHeight,
		input.ResultByteCount, input.ResultSHA256, now,
		input.ID, input.ConversationID, input.TurnID, input.CallID, ToolNameDesktopObserve, now,
	)
	if err != nil {
		return ToolExecutionRecord{}, false, fmt.Errorf("completing tool execution: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ToolExecutionRecord{}, false, fmt.Errorf("completing tool execution: %w", err)
	}
	if changed == 0 {
		return ToolExecutionRecord{}, false, nil
	}
	record, err := s.loadChangedSeekDBToolExecution(ctx, input.ID)
	if err != nil {
		return ToolExecutionRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) failToolExecutionSeekDB(ctx context.Context, id, code, message string) (ToolExecutionRecord, bool, error) {
	if err := validateID("execution_id", id); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	if err := validateToolExecutionFailure(code, message); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	now := s.currentUnixMS()
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	result, err := s.seekDB.ExecContext(queryCtx, `
UPDATE tool_executions
SET status = 'failed', error_code = ?, error_message = ?, updated_at_ms = ?
WHERE id = ? AND status = 'pending'`, code, message, now, id)
	if err != nil {
		return ToolExecutionRecord{}, false, fmt.Errorf("failing tool execution: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ToolExecutionRecord{}, false, fmt.Errorf("failing tool execution: %w", err)
	}
	if changed == 0 {
		return ToolExecutionRecord{}, false, nil
	}
	record, err := s.loadChangedSeekDBToolExecution(ctx, id)
	if err != nil {
		return ToolExecutionRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) cancelToolExecutionsForTurnSeekDB(ctx context.Context, conversationID, turnID, code, message string) (int64, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return 0, err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return 0, err
	}
	if err := validateToolExecutionFailure(code, message); err != nil {
		return 0, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	result, err := s.seekDB.ExecContext(queryCtx, `
UPDATE tool_executions
SET status = 'cancelled', error_code = ?, error_message = ?, updated_at_ms = ?
WHERE conversation_id = ? AND turn_id = ? AND status = 'pending'`,
		code, message, s.currentUnixMS(), conversationID, turnID,
	)
	if err != nil {
		return 0, fmt.Errorf("cancelling tool executions: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) expireToolExecutionsSeekDB(ctx context.Context, now int64) (int64, error) {
	if now < 0 {
		return 0, errors.New("expiration time is invalid")
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	result, err := s.seekDB.ExecContext(queryCtx, `
UPDATE tool_executions
SET status = 'failed', error_code = 'deadline_exceeded',
    error_message = 'desktop capture deadline exceeded', updated_at_ms = ?
WHERE status = 'pending' AND deadline_at_ms <= ?`, now, now)
	if err != nil {
		return 0, fmt.Errorf("expiring tool executions: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) loadToolExecutionSeekDB(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	if err := validateID("execution_id", id); err != nil {
		return ToolExecutionRecord{}, false, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	record, err := scanSeekDBToolExecution(s.seekDB.QueryRowContext(queryCtx,
		"SELECT "+seekDBToolExecutionColumns+" FROM tool_executions WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return ToolExecutionRecord{}, false, nil
	}
	if err != nil {
		return ToolExecutionRecord{}, false, fmt.Errorf("loading tool execution: %w", err)
	}
	return record, true, nil
}

func (s *Store) settleRecoveredToolExecutionsSeekDB(ctx context.Context) (int64, error) {
	now := s.currentUnixMS()
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning recovered tool execution settlement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	lockRows, err := tx.QueryContext(queryCtx, `
SELECT turn.id
FROM conversation_turns turn
INNER JOIN tool_executions execution
  ON execution.conversation_id = turn.conversation_id AND execution.turn_id = turn.id
WHERE execution.tool_name = ?
  AND turn.status IN ('interpreting', 'planning')
  AND execution.status IN ('pending', 'completed')
FOR UPDATE`, ToolNameDesktopObserve)
	if err != nil {
		return 0, fmt.Errorf("locking recovered tool execution turns: %w", err)
	}
	for lockRows.Next() {
		var turnID string
		if err := lockRows.Scan(&turnID); err != nil {
			lockRows.Close()
			return 0, fmt.Errorf("locking recovered tool execution turns: %w", err)
		}
	}
	lockErr := lockRows.Err()
	if closeErr := lockRows.Close(); lockErr != nil {
		return 0, fmt.Errorf("locking recovered tool execution turns: %w", lockErr)
	} else if closeErr != nil {
		return 0, fmt.Errorf("locking recovered tool execution turns: %w", closeErr)
	}
	if _, err := tx.ExecContext(queryCtx, `
UPDATE conversation_turns turn
INNER JOIN tool_executions execution
  ON execution.conversation_id = turn.conversation_id AND execution.turn_id = turn.id
SET execution.status = 'failed',
    execution.error_code = 'core_restarted',
    execution.error_message = 'desktop capture was interrupted by Core restart',
    execution.updated_at_ms = ?
WHERE execution.tool_name = ?
  AND execution.status = 'pending'
  AND turn.status IN ('interpreting', 'planning')`, now, ToolNameDesktopObserve); err != nil {
		return 0, fmt.Errorf("failing recovered pending tool executions: %w", err)
	}
	result, err := tx.ExecContext(queryCtx, `
UPDATE conversation_turns turn
INNER JOIN tool_executions execution
  ON execution.conversation_id = turn.conversation_id AND execution.turn_id = turn.id
SET turn.status = 'failed',
    turn.extraction_state = 'ineligible',
    turn.error_code = 'DESKTOP_CAPTURE_RECOVERY_FAILED',
    turn.error_message = CASE
      WHEN execution.status = 'completed' THEN 'desktop capture evidence was lost during Core restart'
      ELSE 'desktop capture was interrupted by Core restart'
    END,
    turn.error_retryable = 0,
    turn.updated_at_ms = ?
WHERE execution.tool_name = ?
  AND turn.status IN ('interpreting', 'planning')
  AND (
    execution.status = 'completed'
    OR (execution.status = 'failed' AND execution.error_code = 'core_restarted')
  )`, now, ToolNameDesktopObserve)
	if err != nil {
		return 0, fmt.Errorf("failing recovered tool execution turns: %w", err)
	}
	settled, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting recovered tool execution turns: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing recovered tool execution settlement: %w", err)
	}
	return settled, nil
}

func (s *Store) loadChangedSeekDBToolExecution(ctx context.Context, id string) (ToolExecutionRecord, error) {
	record, ok, err := s.loadToolExecutionSeekDB(ctx, id)
	if err != nil {
		return ToolExecutionRecord{}, err
	}
	if !ok {
		return ToolExecutionRecord{}, errors.New("updated tool execution is missing")
	}
	return record, nil
}

func requireSeekDBToolTurn(ctx context.Context, tx *sql.Tx, conversationID, turnID string) error {
	var status string
	err := tx.QueryRowContext(ctx, `
SELECT status FROM conversation_turns
WHERE conversation_id = ? AND id = ? FOR UPDATE`, conversationID, turnID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("turn does not belong to conversation")
	}
	if err != nil {
		return fmt.Errorf("checking turn: %w", err)
	}
	if status != "interpreting" && status != "planning" {
		return errors.New("tool execution requires an active turn")
	}
	return nil
}

func scanSeekDBToolExecution(row scanner) (ToolExecutionRecord, error) {
	var record ToolExecutionRecord
	var status string
	var dispatched sql.NullInt64
	var errorCode, errorMessage, mediaType, hash sql.NullString
	var width, height, byteCount sql.NullInt64
	if err := row.Scan(
		&record.ID, &record.ConversationID, &record.TurnID, &record.CallID, &record.ToolName, &status,
		&record.DeadlineAtUnixMS, &record.AttemptCount, &dispatched, &errorCode, &errorMessage,
		&mediaType, &width, &height, &byteCount, &hash, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS,
	); err != nil {
		return ToolExecutionRecord{}, err
	}
	record.Status = ToolExecutionStatus(status)
	if dispatched.Valid {
		value := dispatched.Int64
		record.LastDispatchedAtUnixMS = &value
	}
	if errorCode.Valid {
		value := errorCode.String
		record.ErrorCode = &value
	}
	if errorMessage.Valid {
		value := errorMessage.String
		record.ErrorMessage = &value
	}
	if mediaType.Valid {
		value := mediaType.String
		record.ResultMediaType = &value
	}
	if width.Valid {
		value := int(width.Int64)
		record.ResultWidth = &value
	}
	if height.Valid {
		value := int(height.Int64)
		record.ResultHeight = &value
	}
	if byteCount.Valid {
		value := int(byteCount.Int64)
		record.ResultByteCount = &value
	}
	if hash.Valid {
		value := hash.String
		record.ResultSHA256 = &value
	}
	return record, nil
}
