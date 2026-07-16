package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const DefaultExtractionBatchLimit = 12
const MaxMemoryMutationsPerBatch = 16

type ExtractionTurn struct {
	TurnID           string `json:"turnId"`
	UserMessage      string `json:"userMessage"`
	AssistantMessage string `json:"assistantMessage"`
}

type ExtractionBatchInput struct {
	BatchID          string                    `json:"batchId"`
	ConversationID   string                    `json:"conversationId"`
	CharacterID      string                    `json:"characterId"`
	Turns            []ExtractionTurn          `json:"turns"`
	ExistingMemories []RetrievedPersonalMemory `json:"existingMemories"`
}

type MemoryMutation struct {
	Operation             string      `json:"operation"`
	MemoryID              string      `json:"memoryId,omitempty"`
	Kind                  string      `json:"kind"`
	Scope                 MemoryScope `json:"scope"`
	Content               string      `json:"content"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
}

type MemoryMutationResult struct {
	Status           string `json:"status"`
	MemoryID         string `json:"memoryId,omitempty"`
	ExistingMemoryID string `json:"existingMemoryId,omitempty"`
}

type MemoryMutationOutput struct {
	Mutations []MemoryMutation `json:"mutations"`
}

// ClaimExtractionBatch claims pending completed turns for background extract.
// Returns nil when there is nothing to claim.
func (s *Store) ClaimExtractionBatch(conversationID string, limit int) (*ExtractionBatchInput, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 12 {
		return nil, errors.New("extraction batch limit must be between 1 and 12")
	}
	db, err := s.openWrite()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("beginning extraction claim transaction: %w", err)
	}
	defer tx.Rollback()

	var characterID string
	if err := tx.QueryRow("SELECT character_id FROM conversations WHERE id = ?1", conversationID).Scan(&characterID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("conversation does not exist")
		}
		return nil, fmt.Errorf("reading conversation character: %w", err)
	}

	rows, err := tx.Query(`
SELECT t.id, t.sequence, u.content, a.content
FROM conversation_turns t
JOIN conversation_messages u ON u.turn_id = t.id AND u.role = 'user'
JOIN conversation_messages a ON a.turn_id = t.id AND a.role = 'assistant'
WHERE t.conversation_id = ?1 AND t.status = 'completed' AND t.extraction_state = 'pending'
ORDER BY t.sequence ASC
LIMIT ?2`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying pending extraction turns: %w", err)
	}
	type claimedTurn struct {
		id, user, assistant string
		sequence            int64
	}
	claimed := make([]claimedTurn, 0)
	for rows.Next() {
		var item claimedTurn
		if err := rows.Scan(&item.id, &item.sequence, &item.user, &item.assistant); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning pending extraction turn: %w", err)
		}
		claimed = append(claimed, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pending extraction turns: %w", err)
	}
	if len(claimed) == 0 {
		return nil, nil
	}

	batchID := newID()
	now := nowUnixMS()
	first := claimed[0].sequence
	last := claimed[len(claimed)-1].sequence
	if _, err := tx.Exec(`
INSERT INTO extraction_batches(
  id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence, created_at_ms, updated_at_ms
) VALUES (?1, ?2, ?3, 'running', ?4, ?5, ?6, ?6)`, batchID, conversationID, characterID, first, last, now); err != nil {
		return nil, fmt.Errorf("inserting extraction batch: %w", err)
	}

	turns := make([]ExtractionTurn, 0, len(claimed))
	for _, item := range claimed {
		changed, err := tx.Exec(`
UPDATE conversation_turns
SET extraction_state = 'claimed', updated_at_ms = ?3
WHERE id = ?1 AND conversation_id = ?2 AND extraction_state = 'pending'`, item.id, conversationID, now)
		if err != nil {
			return nil, fmt.Errorf("claiming extraction turn: %w", err)
		}
		count, err := changed.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("checking claimed extraction turn: %w", err)
		}
		if count != 1 {
			return nil, errors.New("pending extraction turn was claimed by another batch")
		}
		if _, err := tx.Exec(
			"INSERT INTO extraction_batch_turns(batch_id, turn_id, turn_sequence) VALUES (?1, ?2, ?3)",
			batchID, item.id, item.sequence,
		); err != nil {
			return nil, fmt.Errorf("recording extraction batch turn: %w", err)
		}
		turns = append(turns, ExtractionTurn{
			TurnID:           item.id,
			UserMessage:      item.user,
			AssistantMessage: item.assistant,
		})
	}

	queryParts := make([]string, 0, len(turns)*2)
	for _, turn := range turns {
		queryParts = append(queryParts, turn.UserMessage, turn.AssistantMessage)
	}
	existing := []RetrievedPersonalMemory{}
	ftsQuery, err := buildFTSQuery(strings.Join(queryParts, " "))
	if err != nil {
		return nil, err
	}
	if ftsQuery != "" {
		remaining := maxRetrievedContextChars
		existing, err = retrievePersonal(tx, characterID, ftsQuery, &remaining)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing extraction claim: %w", err)
	}
	return &ExtractionBatchInput{
		BatchID:          batchID,
		ConversationID:   conversationID,
		CharacterID:      characterID,
		Turns:            turns,
		ExistingMemories: existing,
	}, nil
}

func (s *Store) PendingExtractionTurnCount(conversationID string) (uint64, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return 0, err
	}
	db, err := s.openReadOnly()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var count int64
	if err := db.QueryRow(`
SELECT COUNT(*) FROM conversation_turns
WHERE conversation_id = ?1 AND status = 'completed' AND extraction_state = 'pending'`, conversationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting pending extraction turns: %w", err)
	}
	if count < 0 {
		return 0, errors.New("pending extraction turn count is invalid")
	}
	return uint64(count), nil
}

func (s *Store) FailExtractionBatch(batchID, code, message string, retryable bool) error {
	if err := validateID("batch_id", batchID); err != nil {
		return err
	}
	if code == "" || message == "" {
		return errors.New("extraction failure code and message are required")
	}
	db, err := s.openWrite()
	if err != nil {
		return err
	}
	defer db.Close()
	retryableInt := 0
	if retryable {
		retryableInt = 1
	}
	changed, err := db.Exec(`
UPDATE extraction_batches
SET status = 'failed', error_code = ?2, error_message = ?3, error_retryable = ?4, updated_at_ms = ?5
WHERE id = ?1 AND status = 'running'`, batchID, code, message, retryableInt, nowUnixMS())
	if err != nil {
		return fmt.Errorf("failing extraction batch: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking extraction failure: %w", err)
	}
	if count != 1 {
		return errors.New("extraction batch is not running")
	}
	return nil
}

func (s *Store) CompleteExtractionBatch(batchID string) error {
	if err := validateID("batch_id", batchID); err != nil {
		return err
	}
	db, err := s.openWrite()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning extraction completion transaction: %w", err)
	}
	defer tx.Rollback()
	if err := succeedExtractionBatch(tx, batchID, nowUnixMS()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CommitMemoryMutations(
	batchID string,
	characterID string,
	allowedMemoryIDs []string,
	mutations []MemoryMutation,
) ([]MemoryMutationResult, error) {
	if err := validateID("batch_id", batchID); err != nil {
		return nil, err
	}
	if err := validateID("character_id", characterID); err != nil {
		return nil, err
	}
	if len(mutations) > MaxMemoryMutationsPerBatch {
		return nil, errors.New("extraction batch exceeds memory mutation limit")
	}
	for i := range mutations {
		if err := validateMemoryMutation(&mutations[i], characterID); err != nil {
			return nil, err
		}
	}
	allowed := make(map[string]struct{}, len(allowedMemoryIDs))
	for _, id := range allowedMemoryIDs {
		allowed[id] = struct{}{}
	}

	db, err := s.openWrite()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("beginning memory mutation transaction: %w", err)
	}
	defer tx.Rollback()

	var conversationID, batchCharacterID, sourceTurnID string
	err = tx.QueryRow(`
SELECT b.conversation_id, b.character_id, bt.turn_id
FROM extraction_batches b
JOIN extraction_batch_turns bt ON bt.batch_id = b.id
WHERE b.id = ?1 AND b.status = 'running'
ORDER BY bt.turn_sequence DESC LIMIT 1`, batchID).Scan(&conversationID, &batchCharacterID, &sourceTurnID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("extraction batch does not exist or is not running")
		}
		return nil, fmt.Errorf("reading running extraction batch: %w", err)
	}
	if batchCharacterID != characterID {
		return nil, errors.New("extraction batch does not belong to character")
	}

	now := nowUnixMS()
	results := make([]MemoryMutationResult, 0, len(mutations))
	for _, mutation := range mutations {
		switch mutation.Operation {
		case "create":
			if existingID, err := findDuplicateMemory(tx, mutation.Kind, mutation.Scope, mutation.Content); err != nil {
				return nil, err
			} else if existingID != "" {
				results = append(results, MemoryMutationResult{Status: "no_change", ExistingMemoryID: existingID})
				continue
			}
			record, err := insertPersonalMemory(
				tx, newID(), mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints,
				conversationID, sourceTurnID, nil, now,
			)
			if err != nil {
				return nil, err
			}
			results = append(results, MemoryMutationResult{Status: "applied", MemoryID: record.ID})
		case "supersede":
			if _, ok := allowed[mutation.MemoryID]; !ok {
				return nil, errors.New("supersede references a memory id not provided to the batch")
			}
			if err := requireActiveMemoryScope(tx, mutation.MemoryID, mutation.Kind, mutation.Scope); err != nil {
				return nil, err
			}
			if existingID, err := findDuplicateMemory(tx, mutation.Kind, mutation.Scope, mutation.Content); err != nil {
				return nil, err
			} else if existingID != "" && existingID != mutation.MemoryID {
				results = append(results, MemoryMutationResult{Status: "no_change", ExistingMemoryID: existingID})
				continue
			}
			if _, err := tx.Exec(
				"UPDATE personal_memories SET status = 'superseded', updated_at_ms = ?2 WHERE id = ?1 AND status = 'active'",
				mutation.MemoryID, now,
			); err != nil {
				return nil, fmt.Errorf("superseding personal memory: %w", err)
			}
			supersedesID := mutation.MemoryID
			record, err := insertPersonalMemory(
				tx, newID(), mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints,
				conversationID, sourceTurnID, &supersedesID, now,
			)
			if err != nil {
				return nil, err
			}
			results = append(results, MemoryMutationResult{Status: "applied", MemoryID: record.ID})
		default:
			return nil, fmt.Errorf("unsupported memory mutation operation %q", mutation.Operation)
		}
	}
	if err := succeedExtractionBatch(tx, batchID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing memory mutations: %w", err)
	}
	return results, nil
}

func succeedExtractionBatch(tx *sql.Tx, batchID string, now int64) error {
	if _, err := tx.Exec(`
UPDATE conversation_turns
SET extraction_state = 'processed', updated_at_ms = ?2
WHERE id IN (SELECT turn_id FROM extraction_batch_turns WHERE batch_id = ?1)
  AND extraction_state = 'claimed'`, batchID, now); err != nil {
		return fmt.Errorf("marking extraction turns processed: %w", err)
	}
	changed, err := tx.Exec(`
UPDATE extraction_batches
SET status = 'succeeded', updated_at_ms = ?2
WHERE id = ?1 AND status = 'running'`, batchID, now)
	if err != nil {
		return fmt.Errorf("succeeding extraction batch: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking extraction success: %w", err)
	}
	if count != 1 {
		return errors.New("extraction batch is not running")
	}
	return nil
}

func validateMemoryMutation(mutation *MemoryMutation, characterID string) error {
	if mutation == nil {
		return errors.New("memory mutation is required")
	}
	if mutation.Operation != "create" && mutation.Operation != "supersede" {
		return errors.New("memory mutation operation must be create or supersede")
	}
	if mutation.Operation == "supersede" {
		if err := validateID("memory_id", mutation.MemoryID); err != nil {
			return err
		}
	}
	if err := validateMemoryInput(mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints); err != nil {
		return err
	}
	if strings.TrimSpace(mutation.Content) != mutation.Content {
		return errors.New("memory mutation content must not include leading or trailing whitespace")
	}
	if mutation.Scope.Type == "unassigned_legacy" {
		return errors.New("automatic extraction cannot create or modify legacy relationship memories")
	}
	if mutation.Kind == "relationship" && (mutation.Scope.Type != "character" || mutation.Scope.CharacterID != characterID) {
		return errors.New("relationship mutation does not belong to the current character")
	}
	return nil
}

type personalMemoryQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func findDuplicateMemory(db personalMemoryQuerier, kind string, scope MemoryScope, content string) (string, error) {
	scopeKind, characterID, _ := memoryScopeColumns(scope)
	var rows *sql.Rows
	var err error
	if characterID == nil {
		rows, err = db.Query(`
SELECT id, content FROM personal_memories
WHERE kind = ?1 AND scope_kind = ?2 AND character_id IS NULL
  AND status = 'active' AND review_status = 'ready'
ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind)
	} else {
		rows, err = db.Query(`
SELECT id, content FROM personal_memories
WHERE kind = ?1 AND scope_kind = ?2 AND character_id = ?3
  AND status = 'active' AND review_status = 'ready'
ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind, *characterID)
	}
	if err != nil {
		return "", fmt.Errorf("querying duplicate memories: %w", err)
	}
	defer rows.Close()
	normalized := normalizeMemoryContent(content)
	for rows.Next() {
		var id, existing string
		if err := rows.Scan(&id, &existing); err != nil {
			return "", fmt.Errorf("scanning duplicate memory: %w", err)
		}
		if normalizeMemoryContent(existing) == normalized {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterating duplicate memories: %w", err)
	}
	return "", nil
}

func requireActiveMemoryScope(db personalMemoryQuerier, memoryID, kind string, scope MemoryScope) error {
	current, err := personalMemoryByID(db, memoryID)
	if err != nil {
		return err
	}
	if current.Status != "active" || current.Kind != kind || current.Scope.Type != scope.Type || current.Scope.CharacterID != scope.CharacterID {
		return errors.New("supersede target memory status, kind, or scope does not match")
	}
	return nil
}

func normalizeMemoryContent(content string) string {
	return strings.Join(strings.Fields(content), " ")
}
