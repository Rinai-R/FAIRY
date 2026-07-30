package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type ExtractionTurnRow struct {
	ID        string
	Sequence  int64
	User      string
	Assistant string
}

func BuildExtractionRetrievalProjection(turns []ExtractionTurn) []string {
	fragments := make([]string, 0, len(turns)*2)
	remaining := MaxFTSQueryChars
	for _, turn := range turns {
		for _, field := range []string{turn.UserMessage, turn.AssistantMessage} {
			for _, token := range strings.FieldsFunc(field, unicode.IsSpace) {
				token = strings.TrimSpace(token)
				runes := []rune(token)
				if len(runes) > extractionProjectionFragmentRunes {
					runes = runes[:extractionProjectionFragmentRunes]
				}
				if len(runes) > remaining {
					runes = runes[:remaining]
				}
				fragment := string(runes)
				if fragment == "" {
					continue
				}
				usable, err := BuildFTSQuery(fragment)
				if err != nil || usable == "" {
					continue
				}
				fragments = append(fragments, fragment)
				remaining -= len(runes)
				break
			}
			if remaining == 0 {
				return fragments
			}
		}
	}
	return fragments
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

func SucceedExtractionBatch(ctx context.Context, tx pgx.Tx, batchID, workerID string, now int64) error {
	return succeedExtractionClaim(ctx, tx, batchID, workerID, now)
}

func LockRunningExtractionBatch(ctx context.Context, tx pgx.Tx, batchID, workerID string) (conversationID, characterID string, err error) {
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

func LoadExtractionBatchTurnIDs(ctx context.Context, tx pgx.Tx, batchID string) (map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `
SELECT id
FROM conversation_turns
WHERE extraction_claim_id = $1 AND extraction_state = 'claimed'`, batchID)
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

func FindDuplicateMemory(ctx context.Context, db Querier, kind string, scope MemoryScope, content string) (string, error) {
	scopeKind, characterID, _ := MemoryScopeColumns(scope)
	var rows pgx.Rows
	var err error
	if characterID == nil {
		rows, err = db.Query(ctx, `
SELECT id, content FROM personal_memories
WHERE kind = $1 AND scope_kind = $2 AND character_id IS NULL
  AND status = 'active' AND review_status = 'ready'
ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind)
	} else {
		rows, err = db.Query(ctx, `
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
	encoded, err := json.Marshal(evidenceIDs)
	if err != nil {
		return fmt.Errorf("serializing personal memory evidence: %w", err)
	}
	changed, err := tx.Exec(ctx, `
UPDATE personal_memories
SET evidence_ids_json = $2::jsonb, updated_at_ms = GREATEST(updated_at_ms, $3)
WHERE id = $1`, memoryID, encoded, now)
	if err != nil {
		return fmt.Errorf("storing personal memory evidence: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("personal memory does not exist")
	}
	return nil
}

func InsertMemoryContextCoverage(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, turnID, memoryID, resultStatus string,
	now int64,
) error {
	if resultStatus != "applied" && resultStatus != "no_change" {
		return errors.New("memory context coverage result status is invalid")
	}
	changed, err := tx.Exec(ctx, `
INSERT INTO memory_context_coverages(
  conversation_id, turn_id, memory_id, result_status, created_at_ms
)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (conversation_id, turn_id, memory_id)
DO UPDATE SET result_status = excluded.result_status`,
		conversationID, turnID, memoryID, resultStatus, now)
	if err != nil {
		return fmt.Errorf("inserting memory context coverage: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("memory context coverage was not committed")
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
	rows, err := db.Query(ctx, `
SELECT
  COALESCE(turn.extraction_claim_id, turn.id),
  turn.conversation_id,
  conversation.character_id,
  CASE WHEN turn.extraction_state = 'claimed' THEN 'running' ELSE turn.extraction_state END,
  min(turn.sequence),
  max(turn.sequence),
  min(COALESCE(turn.extraction_error_code, '')),
  min(COALESCE(turn.extraction_error_message, '')),
  min(turn.created_at_ms),
  max(turn.updated_at_ms)
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
	records := make([]ExtractionBatchRecord, 0)
	for rows.Next() {
		var record ExtractionBatchRecord
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

func RetryFailedExtractionBatch(ctx context.Context, tx pgx.Tx, id string, now int64) (string, error) {
	var conversationID string
	if err := tx.QueryRow(ctx, `
SELECT conversation_id
FROM conversation_turns
WHERE id = $1 AND extraction_state = 'failed'
FOR UPDATE`, id).Scan(&conversationID); errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("extraction batch is not retryable")
	} else if err != nil {
		return "", fmt.Errorf("reading failed extraction batch: %w", err)
	}
	changed, err := tx.Exec(ctx, `
UPDATE conversation_turns
SET extraction_state = 'pending',
    extraction_attempt_count = 0,
    extraction_next_attempt_at_ms = 0,
    extraction_error_code = NULL,
    extraction_error_message = NULL,
    updated_at_ms = $2
WHERE id = $1 AND extraction_state = 'failed'`, id, now)
	if err != nil {
		return "", fmt.Errorf("cancelling failed extraction batch: %w", err)
	}
	if changed.RowsAffected() != 1 || conversationID == "" {
		return "", errors.New("extraction batch is not retryable")
	}
	return conversationID, nil
}
