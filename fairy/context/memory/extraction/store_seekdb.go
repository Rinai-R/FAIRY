package extraction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

type seekDBWriteStage string

const (
	seekDBStageClaimAfterRecovery      seekDBWriteStage = "claim.after_recovery"
	seekDBStageClaimAfterClaim         seekDBWriteStage = "claim.after_claim"
	seekDBStageClaimBeforeCommit       seekDBWriteStage = "claim.before_commit"
	seekDBStageFailAfterUpdate         seekDBWriteStage = "fail.after_update"
	seekDBStageFailBeforeCommit        seekDBWriteStage = "fail.before_commit"
	seekDBStageRetryAfterUpdate        seekDBWriteStage = "retry.after_update"
	seekDBStageRetryBeforeCommit       seekDBWriteStage = "retry.before_commit"
	seekDBStageSettleAfterEvidenceLock seekDBWriteStage = "settlement.after_evidence_lock"
	seekDBStageSettleAfterSupersede    seekDBWriteStage = "settlement.after_supersede"
	seekDBStageSettleAfterInsert       seekDBWriteStage = "settlement.after_insert"
	seekDBStageSettleAfterEvidence     seekDBWriteStage = "settlement.after_evidence"
	seekDBStageSettleAfterCoverage     seekDBWriteStage = "settlement.after_coverage"
	seekDBStageSettleAfterProcessed    seekDBWriteStage = "settlement.after_processed"
	seekDBStageSettleBeforeCommit      seekDBWriteStage = "settlement.before_commit"
)

type seekDBClaimTurn struct {
	id        string
	sequence  int64
	updatedAt int64
}

type seekDBBatchTurn struct {
	id      string
	attempt int64
}

func (s *Store) claimExtractionTurnsSeekDB(
	ctx context.Context,
	conversationID string,
	limit int,
) (*ClaimedBatch, error) {
	if err := validateASCIIID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := ValidateBatchLimit(limit); err != nil {
		return nil, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning SeekDB extraction claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	characterID, storedTimeFloor, err := s.lockSeekDBExtractionConversation(queryCtx, tx, conversationID)
	if err != nil {
		return nil, err
	}
	wallNow := s.currentUnixMS()
	effectiveNow := max(wallNow, storedTimeFloor)
	if _, err := tx.ExecContext(queryCtx, `
UPDATE conversation_turns
SET extraction_state = CASE
      WHEN extraction_attempt_count >= ? THEN 'failed'
      ELSE 'pending'
    END,
    extraction_claim_id = NULL,
    extraction_lease_owner = NULL,
    extraction_lease_expires_at_ms = NULL,
    extraction_next_attempt_at_ms = 0,
    extraction_error_code = CASE
      WHEN extraction_attempt_count >= ? THEN 'lease_expired'
      ELSE extraction_error_code
    END,
    extraction_error_message = CASE
      WHEN extraction_attempt_count >= ? THEN 'extraction lease expired after maximum attempts'
      ELSE extraction_error_message
    END,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE conversation_id = ?
  AND status = 'completed'
  AND origin = 'user'
  AND extraction_state = 'claimed'
  AND extraction_lease_expires_at_ms <= ?`,
		maxExtractionAttempts, maxExtractionAttempts, maxExtractionAttempts,
		effectiveNow, conversationID, wallNow,
	); err != nil {
		return nil, fmt.Errorf("recovering expired SeekDB extraction claims: %w", err)
	}
	if err := s.runSeekDBWriteHook(seekDBStageClaimAfterRecovery); err != nil {
		return nil, err
	}

	var running int64
	if err := tx.QueryRowContext(queryCtx, `
SELECT COUNT(*)
FROM conversation_turns
WHERE conversation_id = ?
  AND status = 'completed'
  AND origin = 'user'
  AND extraction_state = 'claimed'
  AND extraction_lease_expires_at_ms > ?`, conversationID, wallNow).Scan(&running); err != nil {
		return nil, fmt.Errorf("checking running SeekDB extraction claim: %w", err)
	}
	if running < 0 {
		return nil, errors.New("running SeekDB extraction claim count is invalid")
	}
	if running > 0 {
		if err := s.runSeekDBWriteHook(seekDBStageClaimBeforeCommit); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("committing SeekDB extraction lease recovery: %w", err)
		}
		return nil, nil
	}

	rows, err := tx.QueryContext(queryCtx, `
SELECT id, sequence, updated_at_ms
FROM conversation_turns
WHERE conversation_id = ?
  AND status = 'completed'
  AND origin = 'user'
  AND extraction_state = 'pending'
  AND extraction_next_attempt_at_ms <= ?
  AND extraction_attempt_count < ?
ORDER BY sequence
LIMIT ?
FOR UPDATE`, conversationID, wallNow, maxExtractionAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("selecting SeekDB extraction turns: %w", err)
	}
	selected := make([]seekDBClaimTurn, 0, limit)
	for rows.Next() {
		var item seekDBClaimTurn
		if err := rows.Scan(&item.id, &item.sequence, &item.updatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning SeekDB extraction turn: %w", err)
		}
		if err := validateASCIIID("stored extraction turn id", item.id); err != nil {
			rows.Close()
			return nil, err
		}
		if item.sequence <= 0 || item.updatedAt < 0 {
			rows.Close()
			return nil, errors.New("stored SeekDB extraction turn metadata is invalid")
		}
		if item.updatedAt > effectiveNow {
			effectiveNow = item.updatedAt
		}
		selected = append(selected, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating SeekDB extraction turns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("closing SeekDB extraction turn rows: %w", err)
	}
	if len(selected) == 0 {
		if err := s.runSeekDBWriteHook(seekDBStageClaimBeforeCommit); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("committing empty SeekDB extraction claim: %w", err)
		}
		return nil, nil
	}

	leaseExpiresAt, err := safeExtractionTimeAdd(wallNow, s.jobLeaseDuration.Milliseconds())
	if err != nil {
		return nil, err
	}
	claimID := newID()
	query, arguments := seekDBExtractionClaimUpdate(
		conversationID, selected, claimID, s.workerID, leaseExpiresAt, wallNow, effectiveNow,
	)
	changed, err := tx.ExecContext(queryCtx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("claiming SeekDB extraction turns: %w", err)
	}
	if affected, err := changed.RowsAffected(); err != nil {
		return nil, fmt.Errorf("counting claimed SeekDB extraction turns: %w", err)
	} else if affected != int64(len(selected)) {
		return nil, errors.New("SeekDB extraction turns changed during claim")
	}
	if err := s.runSeekDBWriteHook(seekDBStageClaimAfterClaim); err != nil {
		return nil, err
	}

	messageRows, err := tx.QueryContext(queryCtx, `
SELECT turn.id, turn.sequence, user_message.content, assistant_message.content
FROM conversation_turns AS turn
JOIN conversation_messages AS user_message
  ON user_message.conversation_id = turn.conversation_id
 AND user_message.turn_id = turn.id
 AND user_message.role = 'user'
JOIN conversation_messages AS assistant_message
  ON assistant_message.conversation_id = turn.conversation_id
 AND assistant_message.turn_id = turn.id
 AND assistant_message.role = 'assistant'
WHERE turn.conversation_id = ?
  AND turn.extraction_claim_id = ?
  AND turn.extraction_lease_owner = ?
  AND turn.extraction_state = 'claimed'
ORDER BY turn.sequence`, conversationID, claimID, s.workerID)
	if err != nil {
		return nil, fmt.Errorf("loading claimed SeekDB extraction turns: %w", err)
	}
	turns := make([]Turn, 0, len(selected))
	for messageRows.Next() {
		var turn Turn
		var sequence int64
		if err := messageRows.Scan(&turn.TurnID, &sequence, &turn.UserMessage, &turn.AssistantMessage); err != nil {
			messageRows.Close()
			return nil, fmt.Errorf("scanning claimed SeekDB extraction messages: %w", err)
		}
		if err := validateASCIIID("stored extraction turn id", turn.TurnID); err != nil {
			messageRows.Close()
			return nil, err
		}
		if sequence <= 0 || !utf8.ValidString(turn.UserMessage) || !utf8.ValidString(turn.AssistantMessage) {
			messageRows.Close()
			return nil, errors.New("stored SeekDB extraction messages are invalid")
		}
		turns = append(turns, turn)
	}
	if err := messageRows.Err(); err != nil {
		messageRows.Close()
		return nil, fmt.Errorf("iterating claimed SeekDB extraction messages: %w", err)
	}
	if err := messageRows.Close(); err != nil {
		return nil, fmt.Errorf("closing claimed SeekDB extraction messages: %w", err)
	}
	if len(turns) != len(selected) {
		return nil, errors.New("SeekDB extraction claim is missing completed turn messages")
	}
	if err := s.runSeekDBWriteHook(seekDBStageClaimBeforeCommit); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing SeekDB extraction claim: %w", err)
	}
	return &ClaimedBatch{
		BatchID: claimID, ConversationID: conversationID, CharacterID: characterID, Turns: turns,
	}, nil
}

func (s *Store) pendingExtractionTurnCountSeekDB(ctx context.Context, conversationID string) (uint64, error) {
	if err := validateASCIIID("conversation_id", conversationID); err != nil {
		return 0, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var count int64
	if err := s.seekDB.QueryRowContext(queryCtx, `
SELECT COUNT(*)
FROM conversation_turns
WHERE conversation_id = ?
  AND status = 'completed'
  AND origin = 'user'
  AND extraction_state = 'pending'`, conversationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting pending SeekDB extraction turns: %w", err)
	}
	if count < 0 {
		return 0, errors.New("pending SeekDB extraction turn count is invalid")
	}
	return uint64(count), nil
}

func (s *Store) failExtractionBatchSeekDB(
	ctx context.Context,
	batchID, code, message string,
	retryable bool,
) error {
	if err := validateASCIIID("batch_id", batchID); err != nil {
		return err
	}
	if err := validateASCIIID("extraction failure code", code); err != nil {
		return err
	}
	cleanMessage, err := cleanSeekDBExtractionFailureMessage(message)
	if err != nil {
		return err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return fmt.Errorf("beginning SeekDB extraction failure transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	conversationID, expectedRows, err := seekDBExtractionBatchConversation(
		queryCtx, tx, batchID, s.workerID,
	)
	if err != nil {
		return err
	}
	_, storedTimeFloor, err := s.lockSeekDBExtractionConversation(queryCtx, tx, conversationID)
	if err != nil {
		return err
	}
	wallNow := s.currentUnixMS()
	effectiveNow := max(wallNow, storedTimeFloor)
	rows, err := tx.QueryContext(queryCtx, `
SELECT id, extraction_attempt_count
FROM conversation_turns
WHERE conversation_id = ?
  AND extraction_claim_id = ?
  AND extraction_lease_owner = ?
  AND extraction_state = 'claimed'
ORDER BY sequence
FOR UPDATE`, conversationID, batchID, s.workerID)
	if err != nil {
		return fmt.Errorf("locking SeekDB extraction batch turns: %w", err)
	}
	batchTurns := make([]seekDBBatchTurn, 0, expectedRows)
	for rows.Next() {
		var item seekDBBatchTurn
		if err := rows.Scan(&item.id, &item.attempt); err != nil {
			rows.Close()
			return fmt.Errorf("scanning SeekDB extraction batch turn: %w", err)
		}
		if err := validateASCIIID("stored extraction turn id", item.id); err != nil {
			rows.Close()
			return err
		}
		if item.attempt < 1 || item.attempt > maxExtractionAttempts {
			rows.Close()
			return errors.New("stored SeekDB extraction attempt count is invalid")
		}
		batchTurns = append(batchTurns, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterating SeekDB extraction batch turns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("closing SeekDB extraction batch turns: %w", err)
	}
	if len(batchTurns) == 0 || len(batchTurns) != expectedRows {
		return errors.New("extraction claim is not owned by this worker")
	}
	for _, item := range batchTurns {
		state := "failed"
		nextAttempt := int64(0)
		if retryable && item.attempt < maxExtractionAttempts {
			state = "pending"
			backoff := int64(1000) << max(int64(0), item.attempt-1)
			nextAttempt, err = safeExtractionTimeAdd(wallNow, min(backoff, int64(30000)))
			if err != nil {
				return err
			}
		}
		changed, err := tx.ExecContext(queryCtx, `
UPDATE conversation_turns
SET extraction_state = ?,
    extraction_claim_id = NULL,
    extraction_lease_owner = NULL,
    extraction_lease_expires_at_ms = NULL,
    extraction_next_attempt_at_ms = ?,
    extraction_error_code = ?,
    extraction_error_message = ?,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE id = ?
  AND conversation_id = ?
  AND extraction_claim_id = ?
  AND extraction_lease_owner = ?
  AND extraction_state = 'claimed'`,
			state, nextAttempt, code, cleanMessage, effectiveNow,
			item.id, conversationID, batchID, s.workerID,
		)
		if err != nil {
			return fmt.Errorf("failing SeekDB extraction claim turn: %w", err)
		}
		affected, err := changed.RowsAffected()
		if err != nil {
			return fmt.Errorf("counting failed SeekDB extraction claim turn: %w", err)
		}
		if affected != 1 {
			return errors.New("extraction claim is not owned by this worker")
		}
	}
	if err := s.runSeekDBWriteHook(seekDBStageFailAfterUpdate); err != nil {
		return err
	}
	if err := s.runSeekDBWriteHook(seekDBStageFailBeforeCommit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing SeekDB extraction failure: %w", err)
	}
	return nil
}

func (s *Store) extractionBatchCatalogSeekDB(ctx context.Context, characterID string) (Catalog, error) {
	if err := validateASCIIID("character_id", characterID); err != nil {
		return Catalog{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	running, err := s.listSeekDBExtractionBatches(queryCtx, characterID, "running")
	if err != nil {
		return Catalog{}, err
	}
	failed, err := s.listSeekDBExtractionBatches(queryCtx, characterID, "failed")
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Running: running, Failed: failed}, nil
}

func (s *Store) retryExtractionBatchSeekDB(ctx context.Context, id string) error {
	if err := validateASCIIID("batch_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return fmt.Errorf("beginning SeekDB extraction retry transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var conversationID string
	if err := tx.QueryRowContext(queryCtx, `
SELECT conversation_id
FROM conversation_turns
WHERE id = ? AND extraction_state = 'failed'`, id).Scan(&conversationID); errors.Is(err, sql.ErrNoRows) {
		return errors.New("extraction batch is not retryable")
	} else if err != nil {
		return fmt.Errorf("reading failed SeekDB extraction batch: %w", err)
	}
	if err := validateASCIIID("stored conversation id", conversationID); err != nil {
		return err
	}
	_, storedTimeFloor, err := s.lockSeekDBExtractionConversation(queryCtx, tx, conversationID)
	if err != nil {
		return err
	}
	effectiveNow := max(s.currentUnixMS(), storedTimeFloor)
	var state string
	if err := tx.QueryRowContext(queryCtx, `
SELECT extraction_state
FROM conversation_turns
WHERE id = ? AND conversation_id = ?
FOR UPDATE`, id, conversationID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return errors.New("extraction batch is not retryable")
	} else if err != nil {
		return fmt.Errorf("locking failed SeekDB extraction batch: %w", err)
	}
	if state != "failed" {
		return errors.New("extraction batch is not retryable")
	}
	changed, err := tx.ExecContext(queryCtx, `
UPDATE conversation_turns
SET extraction_state = 'pending',
    extraction_attempt_count = 0,
    extraction_next_attempt_at_ms = 0,
    extraction_error_code = NULL,
    extraction_error_message = NULL,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE id = ? AND conversation_id = ? AND extraction_state = 'failed'`,
		effectiveNow, id, conversationID,
	)
	if err != nil {
		return fmt.Errorf("retrying failed SeekDB extraction batch: %w", err)
	}
	affected, err := changed.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting retried SeekDB extraction batch: %w", err)
	}
	if affected != 1 {
		return errors.New("extraction batch is not retryable")
	}
	if err := s.runSeekDBWriteHook(seekDBStageRetryAfterUpdate); err != nil {
		return err
	}
	if err := s.runSeekDBWriteHook(seekDBStageRetryBeforeCommit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing SeekDB extraction retry: %w", err)
	}
	return nil
}

// lockSeekDBExtractionConversation is the single serialization root for one
// conversation's extraction queue. It returns the stored timestamp floor;
// callers sample wall time only after the root lock is held, then keep physical
// deadlines separate while preventing updated_at from moving backwards.
func (s *Store) lockSeekDBExtractionConversation(
	ctx context.Context,
	tx *sql.Tx,
	conversationID string,
) (string, int64, error) {
	var characterID string
	if err := tx.QueryRowContext(ctx, `
SELECT character_id
FROM conversations
WHERE id = ?
FOR UPDATE`, conversationID).Scan(&characterID); errors.Is(err, sql.ErrNoRows) {
		return "", 0, errors.New("conversation does not exist")
	} else if err != nil {
		return "", 0, fmt.Errorf("locking SeekDB extraction conversation: %w", err)
	}
	if err := validateASCIIID("stored character id", characterID); err != nil {
		return "", 0, err
	}
	var maximumUpdatedAt int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(updated_at_ms), 0)
FROM conversation_turns
WHERE conversation_id = ?`, conversationID).Scan(&maximumUpdatedAt); err != nil {
		return "", 0, fmt.Errorf("reading SeekDB extraction time floor: %w", err)
	}
	if maximumUpdatedAt < 0 {
		return "", 0, errors.New("stored SeekDB extraction timestamp is invalid")
	}
	return characterID, maximumUpdatedAt, nil
}

func seekDBExtractionBatchConversation(
	ctx context.Context,
	tx *sql.Tx,
	batchID, workerID string,
) (string, int, error) {
	var minimumConversation, maximumConversation sql.NullString
	var count int64
	if err := tx.QueryRowContext(ctx, `
SELECT MIN(conversation_id), MAX(conversation_id), COUNT(*)
FROM conversation_turns
WHERE extraction_claim_id = ?
  AND extraction_lease_owner = ?
  AND extraction_state = 'claimed'`, batchID, workerID).Scan(
		&minimumConversation, &maximumConversation, &count,
	); err != nil {
		return "", 0, fmt.Errorf("reading owned SeekDB extraction batch: %w", err)
	}
	if count <= 0 || !minimumConversation.Valid || !maximumConversation.Valid ||
		minimumConversation.String != maximumConversation.String {
		return "", 0, errors.New("extraction claim is not owned by this worker")
	}
	if count > DefaultBatchLimit {
		return "", 0, errors.New("stored SeekDB extraction batch is too large")
	}
	if err := validateASCIIID("stored conversation id", minimumConversation.String); err != nil {
		return "", 0, err
	}
	return minimumConversation.String, int(count), nil
}

func (s *Store) listSeekDBExtractionBatches(
	ctx context.Context,
	characterID, status string,
) ([]BatchRecord, error) {
	rows, err := s.seekDB.QueryContext(ctx, `
SELECT COALESCE(turn.extraction_claim_id, turn.id),
  turn.conversation_id,
  conversation.character_id,
  CASE WHEN turn.extraction_state = 'claimed' THEN 'running' ELSE turn.extraction_state END,
  MIN(turn.sequence),
  MAX(turn.sequence),
  MIN(COALESCE(turn.extraction_error_code, '')),
  MIN(COALESCE(turn.extraction_error_message, '')),
  MIN(turn.created_at_ms),
  MAX(turn.updated_at_ms)
FROM conversation_turns AS turn
JOIN conversations AS conversation ON conversation.id = turn.conversation_id
WHERE conversation.character_id = ?
  AND (CASE WHEN turn.extraction_state = 'claimed' THEN 'running' ELSE turn.extraction_state END) = ?
GROUP BY COALESCE(turn.extraction_claim_id, turn.id),
  turn.conversation_id,
  conversation.character_id,
  CASE WHEN turn.extraction_state = 'claimed' THEN 'running' ELSE turn.extraction_state END
ORDER BY MAX(turn.updated_at_ms) DESC, COALESCE(turn.extraction_claim_id, turn.id)
LIMIT 20`, characterID, status)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB extraction batches: %w", err)
	}
	defer rows.Close()
	records := make([]BatchRecord, 0)
	for rows.Next() {
		var record BatchRecord
		var first, last, createdAt, updatedAt int64
		var code, message string
		if err := rows.Scan(
			&record.ID, &record.ConversationID, &record.CharacterID, &record.Status,
			&first, &last, &code, &message, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning SeekDB extraction batch: %w", err)
		}
		if err := validateASCIIID("stored extraction batch id", record.ID); err != nil {
			return nil, err
		}
		if err := validateASCIIID("stored extraction conversation id", record.ConversationID); err != nil {
			return nil, err
		}
		if err := validateASCIIID("stored extraction character id", record.CharacterID); err != nil {
			return nil, err
		}
		if first <= 0 || last < first || createdAt < 0 || updatedAt < createdAt {
			return nil, errors.New("stored SeekDB extraction batch metadata is invalid")
		}
		if record.Status != status || status != "running" && status != "failed" {
			return nil, errors.New("stored SeekDB extraction batch status is invalid")
		}
		record.FirstTurnSequence = uint64(first)
		record.LastTurnSequence = uint64(last)
		record.CreatedAtUnixMS = createdAt
		record.UpdatedAtUnixMS = updatedAt
		if status == "failed" {
			if err := validateASCIIID("stored extraction failure code", code); err != nil {
				return nil, err
			}
			if strings.TrimSpace(message) != message || message == "" || !utf8.ValidString(message) {
				return nil, errors.New("stored extraction failure message is invalid")
			}
			record.Error = &WireError{Code: code, Message: message, Retryable: true}
		} else if code != "" || message != "" {
			return nil, errors.New("running SeekDB extraction batch contains an error")
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB extraction batches: %w", err)
	}
	return records, nil
}

func seekDBExtractionClaimUpdate(
	conversationID string,
	turns []seekDBClaimTurn,
	claimID, workerID string,
	leaseExpiresAt, dueAt, updatedAt int64,
) (string, []any) {
	placeholders := make([]string, len(turns))
	arguments := make([]any, 0, 8+len(turns))
	arguments = append(arguments, claimID, workerID, leaseExpiresAt, updatedAt, conversationID)
	for index, turn := range turns {
		placeholders[index] = "?"
		arguments = append(arguments, turn.id)
	}
	arguments = append(arguments, dueAt, maxExtractionAttempts)
	return `
UPDATE conversation_turns
SET extraction_state = 'claimed',
    extraction_claim_id = ?,
    extraction_lease_owner = ?,
    extraction_lease_expires_at_ms = ?,
    extraction_attempt_count = extraction_attempt_count + 1,
    extraction_next_attempt_at_ms = 0,
    extraction_error_code = NULL,
    extraction_error_message = NULL,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE conversation_id = ?
  AND id IN (` + strings.Join(placeholders, ",") + `)
  AND status = 'completed'
  AND origin = 'user'
  AND extraction_state = 'pending'
  AND extraction_next_attempt_at_ms <= ?
  AND extraction_attempt_count < ?`, arguments
}

func safeExtractionTimeAdd(base, delta int64) (int64, error) {
	if base < 0 || delta <= 0 || base > math.MaxInt64-delta {
		return 0, errors.New("extraction timestamp exceeds the supported range")
	}
	return base + delta, nil
}

func cleanSeekDBExtractionFailureMessage(message string) (string, error) {
	if !utf8.ValidString(message) {
		return "", errors.New("extraction failure message is invalid")
	}
	var sanitized strings.Builder
	for _, character := range message {
		if unicode.IsControl(character) {
			sanitized.WriteByte(' ')
			continue
		}
		sanitized.WriteRune(character)
	}
	cleaned := strings.Join(strings.Fields(sanitized.String()), " ")
	if cleaned == "" {
		return "", errors.New("extraction failure message is required")
	}
	runes := []rune(cleaned)
	if len(runes) > 500 {
		cleaned = string(runes[:500])
	}
	return cleaned, nil
}

func (s *Store) runSeekDBWriteHook(stage seekDBWriteStage) error {
	if s != nil && s.seekDBWriteHook != nil {
		if err := s.seekDBWriteHook(stage); err != nil {
			return fmt.Errorf("SeekDB extraction write hook %s: %w", stage, err)
		}
	}
	return nil
}
