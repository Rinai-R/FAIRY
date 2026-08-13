package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"unicode"
	"unicode/utf8"

	historyexpr "fairy/context/history/expression"
)

type seekDBTurnWriteStage string

const (
	seekDBTurnStageBeginAfterTurn          seekDBTurnWriteStage = "begin.after_turn"
	seekDBTurnStageBeginAfterMessage       seekDBTurnWriteStage = "begin.after_message"
	seekDBTurnStageBeginBeforeCommit       seekDBTurnWriteStage = "begin.before_commit"
	seekDBTurnStageInitiationAfterTurn     seekDBTurnWriteStage = "initiation.after_turn"
	seekDBTurnStageInitiationAfterEvidence seekDBTurnWriteStage = "initiation.after_evidence"
	seekDBTurnStageInitiationBeforeCommit  seekDBTurnWriteStage = "initiation.before_commit"
	seekDBTurnStageCompleteAfterUpdate     seekDBTurnWriteStage = "complete.after_terminal_update"
	seekDBTurnStageCompleteAfterMessage    seekDBTurnWriteStage = "complete.after_message"
	seekDBTurnStageCompleteBeforeCommit    seekDBTurnWriteStage = "complete.before_commit"
	seekDBTurnStageInterruptAfterUpdate    seekDBTurnWriteStage = "interrupt.after_terminal_update"
	seekDBTurnStageInterruptAfterMessage   seekDBTurnWriteStage = "interrupt.after_message"
	seekDBTurnStageInterruptBeforeCommit   seekDBTurnWriteStage = "interrupt.before_commit"
)

const terminalTurnConflictMessage = "turn does not belong to conversation or is terminal"

func (s *Store) beginTurnSeekDB(ctx context.Context, conversationID, userMessage, correlationMessageID string) (PersistedTurn, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return PersistedTurn{}, err
	}
	if err := ValidateContent("user message", userMessage); err != nil {
		return PersistedTurn{}, err
	}
	if err := ValidateOptionalMessageID(correlationMessageID); err != nil {
		return PersistedTurn{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return PersistedTurn{}, fmt.Errorf("beginning SeekDB user message transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return PersistedTurn{}, err
	}
	turnSequence, err := nextSeekDBSequence(queryCtx, tx, "conversation_turns", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	messageSequence, err := nextSeekDBSequence(queryCtx, tx, "conversation_messages", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	now := s.currentUnixMS()
	turnID, messageID := newID(), newID()
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO conversation_turns(
    id, conversation_id, message_id, sequence, status, origin,
    error_code, error_message, error_retryable, extraction_state,
    extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
    extraction_attempt_count, extraction_next_attempt_at_ms,
    extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, 'interpreting', 'user',
          NULL, NULL, NULL, 'ineligible', NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?)`,
		turnID, conversationID, nullableSeekDBString(correlationMessageID), turnSequence, now, now,
	); err != nil {
		return PersistedTurn{}, fmt.Errorf("creating SeekDB turn: %w", err)
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageBeginAfterTurn); err != nil {
		return PersistedTurn{}, err
	}
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO conversation_messages(
    id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, 'user', ?, '[]', ?)`, messageID, conversationID, turnID, messageSequence, userMessage, now); err != nil {
		return PersistedTurn{}, fmt.Errorf("writing SeekDB user message: %w", err)
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageBeginAfterMessage); err != nil {
		return PersistedTurn{}, err
	}
	if err := touchSeekDBConversation(queryCtx, tx, conversationID, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageBeginBeforeCommit); err != nil {
		return PersistedTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersistedTurn{}, fmt.Errorf("committing SeekDB user message transaction: %w", err)
	}
	return PersistedTurn{ID: turnID, ConversationID: conversationID, UserMessage: MessageRecord{
		ID: messageID, MessageID: correlationMessageID, ConversationID: conversationID,
		TurnID: turnID, Sequence: uint64(messageSequence), Role: "user", Content: userMessage,
		Parts: []historyexpr.Part{}, CreatedAtUnixMS: now,
	}}, nil
}

func (s *Store) beginInitiationTurnSeekDB(ctx context.Context, conversationID string, evidenceIDs []string) (PersistedTurn, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return PersistedTurn{}, err
	}
	if err := validateSeekDBEvidenceIDs(evidenceIDs); err != nil {
		return PersistedTurn{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return PersistedTurn{}, fmt.Errorf("beginning SeekDB initiation transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return PersistedTurn{}, err
	}
	turnSequence, err := nextSeekDBSequence(queryCtx, tx, "conversation_turns", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	now, turnID := s.currentUnixMS(), newID()
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO conversation_turns(
    id, conversation_id, message_id, sequence, status, origin,
    error_code, error_message, error_retryable, extraction_state,
    extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
    extraction_attempt_count, extraction_next_attempt_at_ms,
    extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, NULL, ?, 'interpreting', 'desktop_initiation',
          NULL, NULL, NULL, 'ineligible', NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?)`,
		turnID, conversationID, turnSequence, now, now,
	); err != nil {
		return PersistedTurn{}, fmt.Errorf("creating SeekDB initiation turn: %w", err)
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageInitiationAfterTurn); err != nil {
		return PersistedTurn{}, err
	}
	for _, evidenceID := range evidenceIDs {
		if _, err := tx.ExecContext(queryCtx, `
INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms)
VALUES (?, ?, ?)`, turnID, evidenceID, now); err != nil {
			return PersistedTurn{}, fmt.Errorf("linking SeekDB initiation evidence: %w", err)
		}
		if err := s.runSeekDBTurnHook(seekDBTurnStageInitiationAfterEvidence); err != nil {
			return PersistedTurn{}, err
		}
	}
	if err := touchSeekDBConversation(queryCtx, tx, conversationID, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageInitiationBeforeCommit); err != nil {
		return PersistedTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersistedTurn{}, fmt.Errorf("committing SeekDB initiation turn: %w", err)
	}
	return PersistedTurn{ID: turnID, ConversationID: conversationID}, nil
}

func (s *Store) completeExpressionTurnSeekDB(ctx context.Context, conversationID, turnID, assistantMessage string, parts []historyexpr.Part, extractionEligible bool) (MessageRecord, error) {
	storedParts, partsJSON, err := prepareSeekDBTerminalMessage(conversationID, turnID, assistantMessage, parts)
	if err != nil {
		return MessageRecord{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return MessageRecord{}, fmt.Errorf("beginning SeekDB assistant message transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return MessageRecord{}, err
	}
	messageSequence, err := nextSeekDBSequence(queryCtx, tx, "conversation_messages", conversationID)
	if err != nil {
		return MessageRecord{}, err
	}
	now, messageID := s.currentUnixMS(), newID()
	result, err := tx.ExecContext(queryCtx, `
UPDATE conversation_turns
SET status = 'completed',
    extraction_state = CASE WHEN origin = 'desktop_initiation' OR ? = 0 THEN 'ineligible' ELSE 'pending' END,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE id = ? AND conversation_id = ? AND status IN ('interpreting', 'planning', 'responding')`,
		extractionEligible, now, turnID, conversationID,
	)
	if err != nil {
		return MessageRecord{}, fmt.Errorf("updating SeekDB turn completion: %w", err)
	}
	if err := requireOneAffectedRow(result); err != nil {
		return MessageRecord{}, err
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageCompleteAfterUpdate); err != nil {
		return MessageRecord{}, err
	}
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO conversation_messages(
    id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, 'assistant', ?, ?, ?)`,
		messageID, conversationID, turnID, messageSequence, assistantMessage, string(partsJSON), now,
	); err != nil {
		return MessageRecord{}, fmt.Errorf("writing SeekDB assistant message: %w", err)
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageCompleteAfterMessage); err != nil {
		return MessageRecord{}, err
	}
	if err := touchSeekDBConversation(queryCtx, tx, conversationID, now); err != nil {
		return MessageRecord{}, err
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageCompleteBeforeCommit); err != nil {
		return MessageRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessageRecord{}, fmt.Errorf("committing SeekDB assistant message transaction: %w", err)
	}
	return MessageRecord{
		ID: messageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence),
		Role: "assistant", Content: assistantMessage, Parts: storedParts, CreatedAtUnixMS: now,
	}, nil
}

func (s *Store) interruptExpressionTurnSeekDB(ctx context.Context, conversationID, turnID, publishedPrefix string, parts []historyexpr.Part) (*MessageRecord, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := validateSeekDBIdentifier("turn_id", turnID); err != nil {
		return nil, err
	}
	if publishedPrefix != "" || len(parts) > 0 {
		if err := validateExpressionMessage(publishedPrefix, parts); err != nil {
			return nil, err
		}
	}
	storedParts := append([]historyexpr.Part{}, parts...)
	partsJSON, err := json.Marshal(storedParts)
	if err != nil {
		return nil, fmt.Errorf("encoding interrupted SeekDB expression parts: %w", err)
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning SeekDB interrupted turn transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireSeekDBConversation(queryCtx, tx, conversationID); err != nil {
		return nil, err
	}
	now := s.currentUnixMS()
	result, err := tx.ExecContext(queryCtx, `
UPDATE conversation_turns
SET status = 'interrupted', extraction_state = 'ineligible',
    error_code = NULL, error_message = NULL, error_retryable = NULL,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE id = ? AND conversation_id = ? AND status IN ('interpreting', 'planning', 'responding')`,
		now, turnID, conversationID,
	)
	if err != nil {
		return nil, fmt.Errorf("updating interrupted SeekDB turn: %w", err)
	}
	if err := requireOneAffectedRow(result); err != nil {
		return nil, err
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageInterruptAfterUpdate); err != nil {
		return nil, err
	}
	var assistant *MessageRecord
	if publishedPrefix != "" || len(storedParts) > 0 {
		messageSequence, err := nextSeekDBSequence(queryCtx, tx, "conversation_messages", conversationID)
		if err != nil {
			return nil, err
		}
		messageID := newID()
		if _, err := tx.ExecContext(queryCtx, `
INSERT INTO conversation_messages(
    id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, 'assistant', ?, ?, ?)`,
			messageID, conversationID, turnID, messageSequence, publishedPrefix, string(partsJSON), now,
		); err != nil {
			return nil, fmt.Errorf("writing interrupted SeekDB assistant prefix: %w", err)
		}
		assistant = &MessageRecord{
			ID: messageID, ConversationID: conversationID, TurnID: turnID,
			Sequence: uint64(messageSequence), Role: "assistant", Content: publishedPrefix,
			Parts: storedParts, CreatedAtUnixMS: now,
		}
		if err := s.runSeekDBTurnHook(seekDBTurnStageInterruptAfterMessage); err != nil {
			return nil, err
		}
	}
	if err := touchSeekDBConversation(queryCtx, tx, conversationID, now); err != nil {
		return nil, err
	}
	if err := s.runSeekDBTurnHook(seekDBTurnStageInterruptBeforeCommit); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing interrupted SeekDB turn transaction: %w", err)
	}
	return assistant, nil
}

func (s *Store) failTurnSeekDB(ctx context.Context, conversationID, turnID, code, message string, retryable bool) error {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return err
	}
	if err := validateSeekDBIdentifier("turn_id", turnID); err != nil {
		return err
	}
	if err := validateSeekDBErrorCode(code); err != nil {
		return err
	}
	if err := ValidateContent("error message", message); err != nil {
		return err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	result, err := s.seekDB.ExecContext(queryCtx, `
UPDATE conversation_turns
SET status = 'failed', extraction_state = 'ineligible',
    error_code = ?, error_message = ?, error_retryable = ?,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE id = ? AND conversation_id = ? AND status IN ('interpreting', 'planning', 'responding')`,
		code, message, retryable, s.currentUnixMS(), turnID, conversationID,
	)
	if err != nil {
		return fmt.Errorf("marking SeekDB turn failed: %w", err)
	}
	return requireOneAffectedRow(result)
}

func prepareSeekDBTerminalMessage(conversationID, turnID, content string, parts []historyexpr.Part) ([]historyexpr.Part, []byte, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return nil, nil, err
	}
	if err := validateSeekDBIdentifier("turn_id", turnID); err != nil {
		return nil, nil, err
	}
	if err := validateExpressionMessage(content, parts); err != nil {
		return nil, nil, err
	}
	storedParts := append([]historyexpr.Part{}, parts...)
	partsJSON, err := json.Marshal(storedParts)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding SeekDB assistant expression parts: %w", err)
	}
	return storedParts, partsJSON, nil
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

func nextSeekDBSequence(ctx context.Context, tx *sql.Tx, table, conversationID string) (int64, error) {
	if table != "conversation_turns" && table != "conversation_messages" {
		return 0, fmt.Errorf("reading next sequence from unsupported SeekDB table %q", table)
	}
	var maximum int64
	query := "SELECT COALESCE(MAX(sequence), 0) FROM " + table + " WHERE conversation_id = ?"
	if err := tx.QueryRowContext(ctx, query, conversationID).Scan(&maximum); err != nil {
		return 0, fmt.Errorf("reading next sequence from SeekDB %s: %w", table, err)
	}
	if maximum == math.MaxInt64 {
		return 0, fmt.Errorf("SeekDB %s sequence is exhausted", table)
	}
	return maximum + 1, nil
}

func touchSeekDBConversation(ctx context.Context, tx *sql.Tx, conversationID string, now int64) error {
	_, err := tx.ExecContext(ctx, `
UPDATE conversations SET updated_at_ms = GREATEST(updated_at_ms, ?) WHERE id = ?`, now, conversationID)
	if err != nil {
		return fmt.Errorf("touching SeekDB conversation: %w", err)
	}
	// The conversation row was already selected FOR UPDATE in this transaction.
	// MySQL reports changed rows by default, so an equal or earlier clock value
	// legitimately yields zero affected rows after GREATEST and is not a miss.
	return nil
}

func requireOneAffectedRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading SeekDB affected row count: %w", err)
	}
	if rows != 1 {
		return errors.New(terminalTurnConflictMessage)
	}
	return nil
}

func validateSeekDBEvidenceIDs(evidenceIDs []string) error {
	if len(evidenceIDs) == 0 || len(evidenceIDs) > 8 {
		return errors.New("initiation evidence count is invalid")
	}
	seen := make(map[string]struct{}, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		if err := ValidateEvidenceID(evidenceID); err != nil {
			return err
		}
		if _, exists := seen[evidenceID]; exists {
			return fmt.Errorf("duplicate initiation evidence %q", evidenceID)
		}
		seen[evidenceID] = struct{}{}
	}
	return nil
}

func validateSeekDBErrorCode(code string) error {
	if err := ValidateContent("error code", code); err != nil {
		return err
	}
	if len(code) > 128 || !utf8.ValidString(code) {
		return errors.New("error code is invalid")
	}
	for _, character := range code {
		if character > unicode.MaxASCII || unicode.IsControl(character) {
			return errors.New("error code is invalid")
		}
	}
	return nil
}

func nullableSeekDBString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) runSeekDBTurnHook(stage seekDBTurnWriteStage) error {
	if s != nil && s.seekDBTurnHook != nil {
		if err := s.seekDBTurnHook(stage); err != nil {
			return fmt.Errorf("SeekDB turn transaction interrupted at %s: %w", stage, err)
		}
	}
	return nil
}
