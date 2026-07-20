package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *Store) knowledgeCatalogPostgres(ctx context.Context) (KnowledgeCatalog, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	candidates, err := listKnowledgePostgres(queryCtx, s.pool.Raw(), "candidate")
	if err != nil {
		return KnowledgeCatalog{}, err
	}
	verified, err := listKnowledgePostgres(queryCtx, s.pool.Raw(), "verified")
	if err != nil {
		return KnowledgeCatalog{}, err
	}
	return KnowledgeCatalog{Candidates: candidates, Verified: verified}, nil
}

func (s *Store) confirmKnowledgeCandidatePostgres(ctx context.Context, id string) (KnowledgeRecord, error) {
	if err := validateID("knowledge_id", id); err != nil {
		return KnowledgeRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("beginning knowledge confirmation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	changed, err := tx.Exec(queryCtx, "UPDATE knowledge_entries SET status = 'verified', verification_basis = 'user_confirmed', updated_at_ms = $2 WHERE id = $1 AND status = 'candidate' AND verification_basis = 'unverified' AND NOT EXISTS (SELECT 1 FROM knowledge_sources s WHERE s.knowledge_id = knowledge_entries.id)", id, now)
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("confirming knowledge candidate: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return KnowledgeRecord{}, errors.New("knowledge entry is not a confirmable candidate")
	}
	var topic, statement string
	if err := tx.QueryRow(queryCtx, "SELECT topic, statement FROM knowledge_entries WHERE id = $1", id).Scan(&topic, &statement); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("reading confirmed knowledge content: %w", err)
	}
	if err := enqueueKnowledgeEmbeddingJobPostgres(queryCtx, tx, id, topic, statement, now); err != nil {
		return KnowledgeRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("committing knowledge confirmation transaction: %w", err)
	}
	return knowledgeByIDPostgres(ctx, s.pool.Raw(), id)
}

func (s *Store) tombstoneKnowledgePostgres(ctx context.Context, id string) error {
	if err := validateID("knowledge_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, "UPDATE knowledge_entries SET status = 'tombstone', updated_at_ms = $2 WHERE id = $1 AND status IN ('candidate', 'verified')", id, nowUnixMS())
	if err != nil {
		return fmt.Errorf("tombstoning knowledge: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge entry is not tombstoneable")
	}
	return nil
}

type postgresKnowledgeQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type knowledgeScanner interface {
	Scan(dest ...any) error
}

func listKnowledgePostgres(ctx context.Context, db postgresKnowledgeQuerier, status string) ([]KnowledgeRecord, error) {
	rows, err := db.Query(ctx, "SELECT id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM knowledge_entries WHERE status = $1 ORDER BY updated_at_ms DESC, id ASC LIMIT 20", status)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge catalog: %w", err)
	}
	defer rows.Close()
	records := make([]KnowledgeRecord, 0)
	for rows.Next() {
		record, err := scanKnowledgePostgres(ctx, db, rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge catalog: %w", err)
	}
	return records, nil
}

func knowledgeByIDPostgres(ctx context.Context, db postgresKnowledgeQuerier, id string) (KnowledgeRecord, error) {
	row := db.QueryRow(ctx, "SELECT id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM knowledge_entries WHERE id = $1", id)
	return scanKnowledgePostgres(ctx, db, row)
}

func scanKnowledgePostgres(ctx context.Context, db postgresKnowledgeQuerier, scanner knowledgeScanner) (KnowledgeRecord, error) {
	var record KnowledgeRecord
	var confidence int
	var supersedes pgtype.Text
	if err := scanner.Scan(&record.ID, &record.Topic, &record.Statement, &record.Status, &record.VerificationBasis, &confidence, &record.SourceConversationID, &record.SourceTurnID, &supersedes, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("scanning knowledge: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return KnowledgeRecord{}, errors.New("knowledge confidence is invalid")
	}
	record.ConfidenceBasisPoints = uint16(confidence)
	if supersedes.Valid {
		record.SupersedesID = &supersedes.String
	}
	sources, err := knowledgeSourcesPostgres(ctx, db, record.ID)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	record.Sources = sources
	return record, nil
}

func knowledgeSourcesPostgres(ctx context.Context, db postgresKnowledgeQuerier, id string) ([]AssistantSource, error) {
	rows, err := db.Query(ctx, "SELECT title, url, snippet, rank, fetched_at_ms FROM knowledge_sources WHERE knowledge_id = $1 ORDER BY rank ASC", id)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge sources: %w", err)
	}
	defer rows.Close()
	sources := make([]AssistantSource, 0)
	for rows.Next() {
		var source AssistantSource
		var rank int
		if err := rows.Scan(&source.Title, &source.URL, &source.Snippet, &rank, &source.FetchedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning knowledge source: %w", err)
		}
		if rank < 1 || rank > 5 {
			return nil, errors.New("knowledge source rank is invalid")
		}
		source.Rank = uint8(rank)
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge sources: %w", err)
	}
	return sources, nil
}

func (s *Store) extractionBatchCatalogPostgres(ctx context.Context, characterID string) (ExtractionBatchCatalog, error) {
	if err := validateID("character_id", characterID); err != nil {
		return ExtractionBatchCatalog{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	running, err := listExtractionBatchesPostgres(queryCtx, s.pool.Raw(), characterID, "running")
	if err != nil {
		return ExtractionBatchCatalog{}, err
	}
	failed, err := listExtractionBatchesPostgres(queryCtx, s.pool.Raw(), characterID, "failed")
	if err != nil {
		return ExtractionBatchCatalog{}, err
	}
	return ExtractionBatchCatalog{Running: running, Failed: failed}, nil
}

func (s *Store) retryExtractionBatchPostgres(ctx context.Context, id string) error {
	if err := validateID("batch_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning extraction retry transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	var conversationID string
	if err := tx.QueryRow(queryCtx, "SELECT conversation_id FROM extraction_batches WHERE id = $1 AND status = 'failed' FOR UPDATE", id).Scan(&conversationID); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("extraction batch is not retryable")
	} else if err != nil {
		return fmt.Errorf("reading failed extraction batch: %w", err)
	}
	now := nowUnixMS()
	if _, err := tx.Exec(queryCtx, "UPDATE conversation_turns SET extraction_state = 'pending', updated_at_ms = $2 WHERE id IN (SELECT turn_id FROM extraction_batch_turns WHERE batch_id = $1) AND extraction_state = 'claimed'", id, now); err != nil {
		return fmt.Errorf("releasing extraction batch turns: %w", err)
	}
	changed, err := tx.Exec(queryCtx, "UPDATE extraction_batches SET status = 'cancelled', lease_owner = NULL, lease_expires_at_ms = NULL, updated_at_ms = $2 WHERE id = $1 AND status = 'failed'", id, now)
	if err != nil {
		return fmt.Errorf("cancelling failed extraction batch: %w", err)
	}
	if changed.RowsAffected() != 1 || conversationID == "" {
		return errors.New("extraction batch is not retryable")
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing extraction retry: %w", err)
	}
	return nil
}

func listExtractionBatchesPostgres(ctx context.Context, db postgresKnowledgeQuerier, characterID, status string) ([]ExtractionBatchRecord, error) {
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
