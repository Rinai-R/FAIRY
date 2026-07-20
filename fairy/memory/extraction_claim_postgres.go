package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type postgresExtractionTurn struct {
	id        string
	user      string
	assistant string
	sequence  int64
}

func (s *Store) claimExtractionBatchPostgres(ctx context.Context, conversationID string, limit int) (*ExtractionBatchInput, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > DefaultExtractionBatchLimit {
		return nil, errors.New("extraction batch limit must be between 1 and 12")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning extraction claim transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	var characterID string
	if err := tx.QueryRow(queryCtx, "SELECT character_id FROM conversations WHERE id = $1 FOR UPDATE", conversationID).Scan(&characterID); errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("conversation does not exist")
	} else if err != nil {
		return nil, fmt.Errorf("reading conversation character: %w", err)
	}
	now := nowUnixMS()
	leaseExpires := now + s.jobLeaseDuration.Milliseconds()
	var reclaimedBatchID string
	err = tx.QueryRow(queryCtx, `
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
RETURNING b.id`, conversationID, now, s.workerID, leaseExpires).Scan(&reclaimedBatchID)
	if err == nil {
		input, err := loadExtractionBatchInputPostgres(queryCtx, tx, reclaimedBatchID, conversationID, characterID)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(queryCtx); err != nil {
			return nil, fmt.Errorf("committing extraction reclaim: %w", err)
		}
		return input, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("reclaiming expired extraction batch: %w", err)
	}
	var running bool
	if err := tx.QueryRow(queryCtx, "SELECT EXISTS(SELECT 1 FROM extraction_batches WHERE conversation_id = $1 AND status = 'running')", conversationID).Scan(&running); err != nil {
		return nil, fmt.Errorf("checking running extraction batch: %w", err)
	}
	if running {
		return nil, nil
	}
	claimed, err := selectPendingExtractionTurnsPostgres(queryCtx, tx, conversationID, limit)
	if err != nil {
		return nil, err
	}
	if len(claimed) == 0 {
		return nil, nil
	}
	batchID := newID()
	first := claimed[0].sequence
	last := claimed[len(claimed)-1].sequence
	_, err = tx.Exec(queryCtx, `
INSERT INTO extraction_batches(
  id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence,
  lease_owner, lease_expires_at_ms, attempt_count, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, 'running', $4, $5, $6, $7, 1, $8, $8)`, batchID, conversationID, characterID, first, last, s.workerID, leaseExpires, now)
	if err != nil {
		return nil, fmt.Errorf("inserting extraction batch: %w", err)
	}
	for _, item := range claimed {
		changed, err := tx.Exec(queryCtx, "UPDATE conversation_turns SET extraction_state = 'claimed', updated_at_ms = $3 WHERE id = $1 AND conversation_id = $2 AND extraction_state = 'pending'", item.id, conversationID, now)
		if err != nil {
			return nil, fmt.Errorf("claiming extraction turn: %w", err)
		}
		if changed.RowsAffected() != 1 {
			return nil, errors.New("pending extraction turn was claimed by another batch")
		}
		if _, err := tx.Exec(queryCtx, "INSERT INTO extraction_batch_turns(batch_id, turn_id, turn_sequence) VALUES ($1, $2, $3)", batchID, item.id, item.sequence); err != nil {
			return nil, fmt.Errorf("recording extraction batch turn: %w", err)
		}
	}
	input, err := buildExtractionBatchInputPostgres(queryCtx, tx, batchID, conversationID, characterID, claimed)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing extraction claim: %w", err)
	}
	return input, nil
}

func selectPendingExtractionTurnsPostgres(ctx context.Context, tx pgx.Tx, conversationID string, limit int) ([]postgresExtractionTurn, error) {
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
	claimed := make([]postgresExtractionTurn, 0, limit)
	for rows.Next() {
		var item postgresExtractionTurn
		if err := rows.Scan(&item.id, &item.sequence, &item.user, &item.assistant); err != nil {
			return nil, fmt.Errorf("scanning pending extraction turn: %w", err)
		}
		claimed = append(claimed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pending extraction turns: %w", err)
	}
	return claimed, nil
}

func loadExtractionBatchInputPostgres(ctx context.Context, tx pgx.Tx, batchID, conversationID, characterID string) (*ExtractionBatchInput, error) {
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
	claimed := make([]postgresExtractionTurn, 0)
	for rows.Next() {
		var item postgresExtractionTurn
		if err := rows.Scan(&item.id, &item.sequence, &item.user, &item.assistant); err != nil {
			return nil, fmt.Errorf("scanning extraction batch turn: %w", err)
		}
		claimed = append(claimed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating extraction batch turns: %w", err)
	}
	return buildExtractionBatchInputPostgres(ctx, tx, batchID, conversationID, characterID, claimed)
}

func buildExtractionBatchInputPostgres(ctx context.Context, tx pgx.Tx, batchID, conversationID, characterID string, claimed []postgresExtractionTurn) (*ExtractionBatchInput, error) {
	turns := make([]ExtractionTurn, 0, len(claimed))
	queryParts := make([]string, 0, len(claimed)*2)
	for _, item := range claimed {
		turns = append(turns, ExtractionTurn{TurnID: item.id, UserMessage: item.user, AssistantMessage: item.assistant})
		queryParts = append(queryParts, item.user, item.assistant)
	}
	remaining := maxRetrievedContextChars
	existing, err := retrievePersonalTrigramPostgres(ctx, tx, characterID, strings.Join(queryParts, " "), &remaining)
	if err != nil {
		return nil, err
	}
	return &ExtractionBatchInput{BatchID: batchID, ConversationID: conversationID, CharacterID: characterID, Turns: turns, ExistingMemories: existing}, nil
}

type postgresRetrievalQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func retrievePersonalTrigramPostgres(ctx context.Context, db postgresRetrievalQuerier, characterID, query string, remaining *int) ([]RetrievedPersonalMemory, error) {
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return []RetrievedPersonalMemory{}, nil
	}
	rows, err := db.Query(ctx, `
SELECT id, kind, scope_kind, character_id, content, confidence_basis_points, updated_at_ms
FROM personal_memories
WHERE status = 'active' AND review_status = 'ready'
  AND (scope_kind = 'global' OR (scope_kind = 'character' AND character_id = $1))
  AND (content ILIKE '%' || $2 || '%' OR content OPERATOR(public.%) $2 OR $2 OPERATOR(public.<%) content)
ORDER BY GREATEST(public.similarity(content, $2), public.word_similarity($2, content)) DESC,
         confidence_basis_points DESC,
         updated_at_ms DESC,
         id ASC
LIMIT 64`, characterID, normalized)
	if err != nil {
		return nil, fmt.Errorf("querying retrieved personal memories: %w", err)
	}
	defer rows.Close()
	perKind := make(map[string]int)
	results := make([]RetrievedPersonalMemory, 0)
	for rows.Next() {
		var record RetrievedPersonalMemory
		var scopeKind string
		var character pgtype.Text
		var confidence int
		if err := rows.Scan(&record.ID, &record.Kind, &scopeKind, &character, &record.Content, &confidence, &record.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning retrieved personal memory: %w", err)
		}
		if perKind[record.Kind] >= maxResultsPerKind {
			continue
		}
		length := len([]rune(record.Content))
		if length > *remaining {
			continue
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("retrieved personal memory confidence is invalid")
		}
		*remaining -= length
		perKind[record.Kind]++
		record.Scope = MemoryScope{Type: scopeKind}
		if character.Valid {
			record.Scope.CharacterID = character.String
		}
		record.Layer = personalMemoryLayer(record.Kind, record.Scope)
		record.ConfidenceBasisPoints = uint16(confidence)
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating retrieved personal memories: %w", err)
	}
	return results, nil
}

func (s *Store) pendingExtractionTurnCountPostgres(ctx context.Context, conversationID string) (uint64, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return 0, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var count int64
	if err := s.pool.Raw().QueryRow(queryCtx, "SELECT COUNT(*) FROM conversation_turns WHERE conversation_id = $1 AND status = 'completed' AND extraction_state = 'pending'", conversationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting pending extraction turns: %w", err)
	}
	if count < 0 {
		return 0, errors.New("pending extraction turn count is invalid")
	}
	return uint64(count), nil
}

func (s *Store) failExtractionBatchPostgres(ctx context.Context, batchID, code, message string, retryable bool) error {
	if err := validateID("batch_id", batchID); err != nil {
		return err
	}
	if code == "" || message == "" {
		return errors.New("extraction failure code and message are required")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE extraction_batches
SET status = 'failed', lease_owner = NULL, lease_expires_at_ms = NULL,
    error_code = $2, error_message = $3, error_retryable = $4, updated_at_ms = $5
WHERE id = $1 AND status = 'running' AND lease_owner = $6`, batchID, code, message, retryable, nowUnixMS(), s.workerID)
	if err != nil {
		return fmt.Errorf("failing extraction batch: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("extraction batch is not owned by this worker")
	}
	return nil
}

func (s *Store) completeExtractionBatchPostgres(ctx context.Context, batchID string) error {
	if err := validateID("batch_id", batchID); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning extraction completion transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := succeedExtractionBatchPostgres(queryCtx, tx, batchID, s.workerID, nowUnixMS()); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing extraction completion transaction: %w", err)
	}
	return nil
}
