package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type ExtractionTurnRow struct {
	ID        string
	Sequence  int64
	User      string
	Assistant string
}

func LockConversationCharacter(ctx context.Context, tx pgx.Tx, conversationID string) (string, error) {
	var characterID string
	if err := tx.QueryRow(ctx, "SELECT character_id FROM conversations WHERE id = $1 FOR UPDATE", conversationID).Scan(&characterID); errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("conversation does not exist")
	} else if err != nil {
		return "", fmt.Errorf("reading conversation character: %w", err)
	}
	return characterID, nil
}

func ReclaimExpiredExtractionBatch(ctx context.Context, tx pgx.Tx, conversationID, workerID string, now, leaseExpires int64) (string, error) {
	var reclaimedBatchID string
	err := tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT id FROM extraction_batches
  WHERE conversation_id = $1 AND status = 'running' AND lease_expires_at_ms <= $2
  ORDER BY lease_expires_at_ms ASC, id ASC
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
UPDATE extraction_batches b
SET lease_owner = $3, lease_expires_at_ms = $4,
    attempt_count = b.attempt_count + 1, updated_at_ms = $2
FROM candidate c
WHERE b.id = c.id
RETURNING b.id`, conversationID, now, workerID, leaseExpires).Scan(&reclaimedBatchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reclaiming expired extraction batch: %w", err)
	}
	return reclaimedBatchID, nil
}

func HasRunningExtractionBatch(ctx context.Context, tx pgx.Tx, conversationID string) (bool, error) {
	var running bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM extraction_batches WHERE conversation_id = $1 AND status = 'running')", conversationID).Scan(&running); err != nil {
		return false, fmt.Errorf("checking running extraction batch: %w", err)
	}
	return running, nil
}

func SelectPendingExtractionTurns(ctx context.Context, tx pgx.Tx, conversationID string, limit int) ([]ExtractionTurnRow, error) {
	rows, err := tx.Query(ctx, `
SELECT t.id, t.sequence, u.content, a.content
FROM conversation_turns t
JOIN conversation_messages u ON u.turn_id = t.id AND u.role = 'user'
JOIN conversation_messages a ON a.turn_id = t.id AND a.role = 'assistant'
WHERE t.conversation_id = $1 AND t.status = 'completed' AND t.extraction_state = 'pending'
ORDER BY t.sequence ASC
LIMIT $2
FOR UPDATE OF t SKIP LOCKED`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying pending extraction turns: %w", err)
	}
	defer rows.Close()
	claimed := make([]ExtractionTurnRow, 0, limit)
	for rows.Next() {
		var item ExtractionTurnRow
		if err := rows.Scan(&item.ID, &item.Sequence, &item.User, &item.Assistant); err != nil {
			return nil, fmt.Errorf("scanning pending extraction turn: %w", err)
		}
		claimed = append(claimed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pending extraction turns: %w", err)
	}
	return claimed, nil
}

func InsertExtractionBatch(
	ctx context.Context,
	tx pgx.Tx,
	batchID, conversationID, characterID, workerID string,
	firstSequence, lastSequence int64,
	leaseExpires, now int64,
) error {
	_, err := tx.Exec(ctx, `
INSERT INTO extraction_batches(
  id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence,
  lease_owner, lease_expires_at_ms, attempt_count, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, 'running', $4, $5, $6, $7, 1, $8, $8)`, batchID, conversationID, characterID, firstSequence, lastSequence, workerID, leaseExpires, now)
	if err != nil {
		return fmt.Errorf("inserting extraction batch: %w", err)
	}
	return nil
}

func ClaimExtractionTurns(ctx context.Context, tx pgx.Tx, batchID, conversationID string, now int64, turns []ExtractionTurnRow) error {
	for _, item := range turns {
		changed, err := tx.Exec(ctx, "UPDATE conversation_turns SET extraction_state = 'claimed', updated_at_ms = $3 WHERE id = $1 AND conversation_id = $2 AND extraction_state = 'pending'", item.ID, conversationID, now)
		if err != nil {
			return fmt.Errorf("claiming extraction turn: %w", err)
		}
		if changed.RowsAffected() != 1 {
			return errors.New("pending extraction turn was claimed by another batch")
		}
		if _, err := tx.Exec(ctx, "INSERT INTO extraction_batch_turns(batch_id, turn_id, turn_sequence) VALUES ($1, $2, $3)", batchID, item.ID, item.Sequence); err != nil {
			return fmt.Errorf("recording extraction batch turn: %w", err)
		}
	}
	return nil
}

func LoadExtractionBatchTurns(ctx context.Context, tx pgx.Tx, batchID string) ([]ExtractionTurnRow, error) {
	rows, err := tx.Query(ctx, `
SELECT t.id, bt.turn_sequence, u.content, a.content
FROM extraction_batch_turns bt
JOIN conversation_turns t ON t.id = bt.turn_id
JOIN conversation_messages u ON u.turn_id = t.id AND u.role = 'user'
JOIN conversation_messages a ON a.turn_id = t.id AND a.role = 'assistant'
WHERE bt.batch_id = $1
ORDER BY bt.turn_sequence ASC`, batchID)
	if err != nil {
		return nil, fmt.Errorf("loading extraction batch turns: %w", err)
	}
	defer rows.Close()
	claimed := make([]ExtractionTurnRow, 0)
	for rows.Next() {
		var item ExtractionTurnRow
		if err := rows.Scan(&item.ID, &item.Sequence, &item.User, &item.Assistant); err != nil {
			return nil, fmt.Errorf("scanning extraction batch turn: %w", err)
		}
		claimed = append(claimed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating extraction batch turns: %w", err)
	}
	return claimed, nil
}

func BuildExtractionBatchInput(
	ctx context.Context,
	tx pgx.Tx,
	batchID, conversationID, characterID string,
	claimed []ExtractionTurnRow,
	normalizeQuery func(string) (string, error),
) (*ExtractionBatchInput, error) {
	turns := make([]ExtractionTurn, 0, len(claimed))
	for _, item := range claimed {
		turns = append(turns, ExtractionTurn{TurnID: item.ID, UserMessage: item.User, AssistantMessage: item.Assistant})
	}
	remaining := maxRetrievedContextChars
	projection := BuildExtractionRetrievalProjection(turns)
	existing, err := RetrievePersonalExtractionProjection(ctx, tx, characterID, projection, &remaining, normalizeQuery)
	if err != nil {
		return nil, err
	}
	return &ExtractionBatchInput{
		BatchID:          batchID,
		ConversationID:   conversationID,
		CharacterID:      characterID,
		Turns:            turns,
		ExistingMemories: existing,
	}, nil
}

func LoadExtractionBatchInput(
	ctx context.Context,
	tx pgx.Tx,
	batchID, conversationID, characterID string,
	normalizeQuery func(string) (string, error),
) (*ExtractionBatchInput, error) {
	claimed, err := LoadExtractionBatchTurns(ctx, tx, batchID)
	if err != nil {
		return nil, err
	}
	return BuildExtractionBatchInput(ctx, tx, batchID, conversationID, characterID, claimed, normalizeQuery)
}

func CountPendingExtractionTurns(ctx context.Context, db RowQuerier, conversationID string) (int64, error) {
	var count int64
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM conversation_turns WHERE conversation_id = $1 AND status = 'completed' AND extraction_state = 'pending'", conversationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting pending extraction turns: %w", err)
	}
	if count < 0 {
		return 0, errors.New("pending extraction turn count is invalid")
	}
	return count, nil
}

func FailExtractionBatch(ctx context.Context, exec interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, batchID, workerID, code, message string, retryable bool, now int64) error {
	changed, err := exec.Exec(ctx, `
UPDATE extraction_batches
SET status = 'failed', lease_owner = NULL, lease_expires_at_ms = NULL,
    error_code = $2, error_message = $3, error_retryable = $4, updated_at_ms = $5
WHERE id = $1 AND status = 'running' AND lease_owner = $6`, batchID, code, message, retryable, now, workerID)
	if err != nil {
		return fmt.Errorf("failing extraction batch: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("extraction batch is not owned by this worker")
	}
	return nil
}

func SucceedExtractionBatch(ctx context.Context, tx pgx.Tx, batchID, workerID string, now int64) error {
	_, err := tx.Exec(ctx, `
UPDATE conversation_turns
SET extraction_state = 'processed', updated_at_ms = $2
WHERE id IN (SELECT turn_id FROM extraction_batch_turns WHERE batch_id = $1)
  AND extraction_state = 'claimed'`, batchID, now)
	if err != nil {
		return fmt.Errorf("marking extraction turns processed: %w", err)
	}
	changed, err := tx.Exec(ctx, `
UPDATE extraction_batches
SET status = 'succeeded', lease_owner = NULL, lease_expires_at_ms = NULL, updated_at_ms = $2
WHERE id = $1 AND status = 'running' AND lease_owner = $3`, batchID, now, workerID)
	if err != nil {
		return fmt.Errorf("succeeding extraction batch: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("extraction batch is not owned by this worker")
	}
	return nil
}

func LockRunningExtractionBatch(ctx context.Context, tx pgx.Tx, batchID string) (conversationID, characterID string, err error) {
	err = tx.QueryRow(ctx, `
SELECT b.conversation_id, b.character_id
FROM extraction_batches b
JOIN extraction_batch_turns bt ON bt.batch_id = b.id
WHERE b.id = $1 AND b.status = 'running'
ORDER BY bt.turn_sequence DESC LIMIT 1
FOR UPDATE OF b`, batchID).Scan(&conversationID, &characterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errors.New("extraction batch does not exist or is not running")
	}
	if err != nil {
		return "", "", fmt.Errorf("reading running extraction batch: %w", err)
	}
	return conversationID, characterID, nil
}

func LoadExtractionBatchTurnIDs(ctx context.Context, tx pgx.Tx, batchID string) (map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `SELECT turn_id FROM extraction_batch_turns WHERE batch_id = $1`, batchID)
	if err != nil {
		return nil, fmt.Errorf("loading extraction batch turns: %w", err)
	}
	defer rows.Close()
	allowedTurnIDs := make(map[string]struct{})
	for rows.Next() {
		var turnID string
		if err := rows.Scan(&turnID); err != nil {
			return nil, fmt.Errorf("scanning extraction batch turn: %w", err)
		}
		allowedTurnIDs[turnID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating extraction batch turns: %w", err)
	}
	return allowedTurnIDs, nil
}

func FindDuplicateMemory(ctx context.Context, tx pgx.Tx, kind string, scope MemoryScope, content string) (string, error) {
	scopeKind, characterID, _ := MemoryScopeColumns(scope)
	var rows pgx.Rows
	var err error
	if characterID == nil {
		rows, err = tx.Query(ctx, `
SELECT id, content FROM personal_memories
WHERE kind = $1 AND scope_kind = $2 AND character_id IS NULL
  AND status = 'active' AND review_status = 'ready'
ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind)
	} else {
		rows, err = tx.Query(ctx, `
SELECT id, content FROM personal_memories
WHERE kind = $1 AND scope_kind = $2 AND character_id = $3
  AND status = 'active' AND review_status = 'ready'
ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind, *characterID)
	}
	if err != nil {
		return "", fmt.Errorf("querying duplicate memories: %w", err)
	}
	defer rows.Close()
	normalized := NormalizeMemoryContent(content)
	for rows.Next() {
		var id, existing string
		if err := rows.Scan(&id, &existing); err != nil {
			return "", fmt.Errorf("scanning duplicate memory: %w", err)
		}
		if NormalizeMemoryContent(existing) == normalized {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterating duplicate memories: %w", err)
	}
	return "", nil
}

func LinkPersonalMemoryEvidence(ctx context.Context, tx pgx.Tx, memoryID, turnID string, now int64) error {
	rows, err := tx.Query(ctx, `SELECT evidence_id FROM conversation_turn_evidence WHERE turn_id = $1 ORDER BY evidence_id`, turnID)
	if err != nil {
		return fmt.Errorf("loading memory source evidence: %w", err)
	}
	evidenceIDs := make([]string, 0, 8)
	for rows.Next() {
		var evidenceID string
		if err := rows.Scan(&evidenceID); err != nil {
			rows.Close()
			return fmt.Errorf("scanning memory source evidence: %w", err)
		}
		evidenceIDs = append(evidenceIDs, evidenceID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterating memory source evidence: %w", err)
	}
	rows.Close()
	for _, evidenceID := range evidenceIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO personal_memory_evidence(memory_id, turn_id, evidence_id, created_at_ms) VALUES ($1, $2, $3, $4)`, memoryID, turnID, evidenceID, now); err != nil {
			return fmt.Errorf("linking personal memory evidence: %w", err)
		}
	}
	return nil
}

func RequireActiveMemoryScope(ctx context.Context, tx pgx.Tx, memoryID, kind string, scope MemoryScope) error {
	current, err := PersonalMemoryByID(ctx, tx, memoryID, true)
	if err != nil {
		return err
	}
	if current.Status != "active" || current.Kind != kind || current.Scope.Type != scope.Type || current.Scope.CharacterID != scope.CharacterID {
		return errors.New("supersede target memory status, kind, or scope does not match")
	}
	return nil
}

func ListExtractionBatches(ctx context.Context, db Querier, characterID, status string) ([]ExtractionBatchRecord, error) {
	rows, err := db.Query(ctx, "SELECT id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence, error_code, error_message, error_retryable, created_at_ms, updated_at_ms FROM extraction_batches WHERE character_id = $1 AND status = $2 ORDER BY updated_at_ms DESC, id ASC LIMIT 20", characterID, status)
	if err != nil {
		return nil, fmt.Errorf("querying extraction batches: %w", err)
	}
	defer rows.Close()
	records := make([]ExtractionBatchRecord, 0)
	for rows.Next() {
		var record ExtractionBatchRecord
		var first, last int64
		var code, message pgtype.Text
		var retryable pgtype.Bool
		if err := rows.Scan(&record.ID, &record.ConversationID, &record.CharacterID, &record.Status, &first, &last, &code, &message, &retryable, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning extraction batch: %w", err)
		}
		record.FirstTurnSequence = uint64(first)
		record.LastTurnSequence = uint64(last)
		if code.Valid && message.Valid && retryable.Valid {
			record.Error = &WireError{Code: code.String, Message: message.String, Retryable: retryable.Bool}
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating extraction batches: %w", err)
	}
	return records, nil
}

func RetryFailedExtractionBatch(ctx context.Context, tx pgx.Tx, id string, now int64) (string, error) {
	var conversationID string
	if err := tx.QueryRow(ctx, "SELECT conversation_id FROM extraction_batches WHERE id = $1 AND status = 'failed' FOR UPDATE", id).Scan(&conversationID); errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("extraction batch is not retryable")
	} else if err != nil {
		return "", fmt.Errorf("reading failed extraction batch: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE conversation_turns SET extraction_state = 'pending', updated_at_ms = $2 WHERE id IN (SELECT turn_id FROM extraction_batch_turns WHERE batch_id = $1) AND extraction_state = 'claimed'", id, now); err != nil {
		return "", fmt.Errorf("releasing extraction batch turns: %w", err)
	}
	changed, err := tx.Exec(ctx, "UPDATE extraction_batches SET status = 'cancelled', lease_owner = NULL, lease_expires_at_ms = NULL, updated_at_ms = $2 WHERE id = $1 AND status = 'failed'", id, now)
	if err != nil {
		return "", fmt.Errorf("cancelling failed extraction batch: %w", err)
	}
	if changed.RowsAffected() != 1 || conversationID == "" {
		return "", errors.New("extraction batch is not retryable")
	}
	return conversationID, nil
}
