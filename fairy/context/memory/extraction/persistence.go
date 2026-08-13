package extraction

import (
	"context"
	"errors"
	"fmt"

	"fairy/context/memory/personal"

	"github.com/jackc/pgx/v5"
)

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type TurnRow struct {
	ID        string
	Sequence  int64
	User      string
	Assistant string
}

type ExistingRetriever func(projection []string, remaining *int) ([]personal.Retrieved, error)

func BuildBatchInput(batchID, conversationID, characterID string, claimed []TurnRow, retrieve ExistingRetriever) (*BatchInput, error) {
	turns := make([]Turn, 0, len(claimed))
	for _, item := range claimed {
		turns = append(turns, Turn{TurnID: item.ID, UserMessage: item.User, AssistantMessage: item.Assistant})
	}
	remaining := personal.MaxContentRunes
	existing, err := retrieve(BuildRetrievalProjection(turns), &remaining)
	if err != nil {
		return nil, err
	}
	return &BatchInput{
		BatchID: batchID, ConversationID: conversationID, CharacterID: characterID,
		Turns: turns, ExistingMemories: existing,
	}, nil
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

func LockRunningBatch(ctx context.Context, tx pgx.Tx, batchID, workerID string) (conversationID, characterID string, err error) {
	err = tx.QueryRow(ctx, `
SELECT turn.conversation_id, conversation.character_id
FROM conversation_turns AS turn
JOIN conversations AS conversation ON conversation.id = turn.conversation_id
WHERE turn.extraction_claim_id = $1
  AND turn.extraction_state = 'claimed'
  AND turn.extraction_lease_owner = $2
ORDER BY turn.sequence
LIMIT 1
FOR UPDATE OF turn`, batchID, workerID).Scan(&conversationID, &characterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errors.New("extraction batch does not exist or is not running")
	}
	if err != nil {
		return "", "", fmt.Errorf("reading running extraction batch: %w", err)
	}
	return conversationID, characterID, nil
}

func LoadBatchTurnIDs(ctx context.Context, tx pgx.Tx, batchID string) (map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM conversation_turns WHERE extraction_claim_id = $1 AND extraction_state = 'claimed'`, batchID)
	if err != nil {
		return nil, fmt.Errorf("loading extraction batch turns: %w", err)
	}
	defer rows.Close()
	allowed := make(map[string]struct{})
	for rows.Next() {
		var turnID string
		if err := rows.Scan(&turnID); err != nil {
			return nil, fmt.Errorf("scanning extraction batch turn: %w", err)
		}
		allowed[turnID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating extraction batch turns: %w", err)
	}
	return allowed, nil
}

func LoadTurnEvidenceIDs(ctx context.Context, tx pgx.Tx, turnID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT evidence_id FROM conversation_turn_evidence WHERE turn_id = $1 ORDER BY evidence_id`, turnID)
	if err != nil {
		return nil, fmt.Errorf("loading memory source evidence: %w", err)
	}
	evidenceIDs := make([]string, 0, 8)
	for rows.Next() {
		var evidenceID string
		if err := rows.Scan(&evidenceID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning memory source evidence: %w", err)
		}
		evidenceIDs = append(evidenceIDs, evidenceID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating memory source evidence: %w", err)
	}
	rows.Close()
	return evidenceIDs, nil
}

func InsertCoverage(ctx context.Context, tx pgx.Tx, conversationID, turnID, memoryID, resultStatus string, now int64) error {
	if resultStatus != "applied" && resultStatus != "no_change" {
		return errors.New("memory context coverage result status is invalid")
	}
	changed, err := tx.Exec(ctx, `
INSERT INTO memory_context_coverages(conversation_id, turn_id, memory_id, result_status, created_at_ms)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (conversation_id, turn_id, memory_id)
DO UPDATE SET
  result_status = CASE
    WHEN memory_context_coverages.result_status = 'applied' OR excluded.result_status = 'applied'
      THEN 'applied'
    ELSE 'no_change'
  END,
  created_at_ms = LEAST(memory_context_coverages.created_at_ms, excluded.created_at_ms)`,
		conversationID, turnID, memoryID, resultStatus, now)
	if err != nil {
		return fmt.Errorf("inserting memory context coverage: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("memory context coverage was not committed")
	}
	return nil
}

func SucceedBatch(ctx context.Context, tx pgx.Tx, batchID, workerID string, now int64) error {
	changed, err := tx.Exec(ctx, `
UPDATE conversation_turns
SET extraction_state = 'processed', extraction_claim_id = NULL, extraction_lease_owner = NULL,
    extraction_lease_expires_at_ms = NULL, extraction_next_attempt_at_ms = 0,
    extraction_error_code = NULL, extraction_error_message = NULL, updated_at_ms = $3
WHERE extraction_claim_id = $1 AND extraction_state = 'claimed' AND extraction_lease_owner = $2`, batchID, workerID, now)
	if err != nil {
		return fmt.Errorf("completing extraction claim: %w", err)
	}
	if changed.RowsAffected() == 0 {
		return errors.New("extraction claim is not owned by this worker")
	}
	return nil
}

func ListBatches(ctx context.Context, db Querier, characterID, status string) ([]BatchRecord, error) {
	rows, err := db.Query(ctx, `
SELECT COALESCE(turn.extraction_claim_id, turn.id), turn.conversation_id, conversation.character_id,
  CASE WHEN turn.extraction_state = 'claimed' THEN 'running' ELSE turn.extraction_state END,
  min(turn.sequence), max(turn.sequence), min(COALESCE(turn.extraction_error_code, '')),
  min(COALESCE(turn.extraction_error_message, '')), min(turn.created_at_ms), max(turn.updated_at_ms)
FROM conversation_turns AS turn
JOIN conversations AS conversation ON conversation.id = turn.conversation_id
WHERE conversation.character_id = $1
  AND (CASE WHEN turn.extraction_state = 'claimed' THEN 'running' ELSE turn.extraction_state END) = $2
GROUP BY COALESCE(turn.extraction_claim_id, turn.id), turn.conversation_id, conversation.character_id,
  CASE WHEN turn.extraction_state = 'claimed' THEN 'running' ELSE turn.extraction_state END
ORDER BY max(turn.updated_at_ms) DESC, COALESCE(turn.extraction_claim_id, turn.id)
LIMIT 20`, characterID, status)
	if err != nil {
		return nil, fmt.Errorf("querying extraction batches: %w", err)
	}
	defer rows.Close()
	records := make([]BatchRecord, 0)
	for rows.Next() {
		var record BatchRecord
		var first, last int64
		var code, message string
		if err := rows.Scan(&record.ID, &record.ConversationID, &record.CharacterID, &record.Status, &first, &last, &code, &message, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning extraction batch: %w", err)
		}
		record.FirstTurnSequence = uint64(first)
		record.LastTurnSequence = uint64(last)
		if code != "" && message != "" {
			record.Error = &WireError{Code: code, Message: message, Retryable: true}
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating extraction batches: %w", err)
	}
	return records, nil
}

func RetryFailedBatch(ctx context.Context, tx pgx.Tx, id string, now int64) (string, error) {
	var conversationID string
	if err := tx.QueryRow(ctx, `SELECT conversation_id FROM conversation_turns WHERE id = $1 AND extraction_state = 'failed' FOR UPDATE`, id).Scan(&conversationID); errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("extraction batch is not retryable")
	} else if err != nil {
		return "", fmt.Errorf("reading failed extraction batch: %w", err)
	}
	changed, err := tx.Exec(ctx, `
UPDATE conversation_turns
SET extraction_state = 'pending', extraction_attempt_count = 0, extraction_next_attempt_at_ms = 0,
    extraction_error_code = NULL, extraction_error_message = NULL, updated_at_ms = $2
WHERE id = $1 AND extraction_state = 'failed'`, id, now)
	if err != nil {
		return "", fmt.Errorf("cancelling failed extraction batch: %w", err)
	}
	if changed.RowsAffected() != 1 || conversationID == "" {
		return "", errors.New("extraction batch is not retryable")
	}
	return conversationID, nil
}
