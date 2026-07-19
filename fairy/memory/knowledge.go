package memory

import (
	"database/sql"
	"errors"
	"fmt"
)

type AssistantSource struct {
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type KnowledgeRecord struct {
	ID                    string            `json:"id"`
	Topic                 string            `json:"topic"`
	Statement             string            `json:"statement"`
	Status                string            `json:"status"`
	VerificationBasis     string            `json:"verificationBasis"`
	ConfidenceBasisPoints uint16            `json:"confidenceBasisPoints"`
	SourceConversationID  string            `json:"sourceConversationId"`
	SourceTurnID          string            `json:"sourceTurnId"`
	SupersedesID          *string           `json:"supersedesId"`
	Sources               []AssistantSource `json:"sources"`
	CreatedAtUnixMS       int64             `json:"createdAtUnixMs"`
	UpdatedAtUnixMS       int64             `json:"updatedAtUnixMs"`
}

type KnowledgeCatalog struct {
	Candidates []KnowledgeRecord `json:"candidates"`
	Verified   []KnowledgeRecord `json:"verified"`
}

type WireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type ExtractionBatchRecord struct {
	ID                string     `json:"id"`
	ConversationID    string     `json:"conversationId"`
	CharacterID       string     `json:"characterId"`
	Status            string     `json:"status"`
	FirstTurnSequence uint64     `json:"firstTurnSequence"`
	LastTurnSequence  uint64     `json:"lastTurnSequence"`
	Error             *WireError `json:"error"`
	CreatedAtUnixMS   int64      `json:"createdAtUnixMs"`
	UpdatedAtUnixMS   int64      `json:"updatedAtUnixMs"`
}

type ExtractionBatchCatalog struct {
	Running []ExtractionBatchRecord `json:"running"`
	Failed  []ExtractionBatchRecord `json:"failed"`
}

func (s *Store) KnowledgeCatalog() (KnowledgeCatalog, error) {
	db, err := s.openReadOnly()
	if err != nil {
		return KnowledgeCatalog{}, err
	}
	defer db.Close()
	candidates, err := listKnowledge(db, "candidate")
	if err != nil {
		return KnowledgeCatalog{}, err
	}
	verified, err := listKnowledge(db, "verified")
	if err != nil {
		return KnowledgeCatalog{}, err
	}
	return KnowledgeCatalog{Candidates: candidates, Verified: verified}, nil
}

func (s *Store) ConfirmKnowledgeCandidate(id string) (KnowledgeRecord, error) {
	if err := validateID("knowledge_id", id); err != nil {
		return KnowledgeRecord{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return KnowledgeRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("beginning knowledge confirmation transaction: %w", err)
	}
	defer tx.Rollback()
	changed, err := tx.Exec("UPDATE knowledge_entries SET status = 'verified', verification_basis = 'user_confirmed', updated_at_ms = ?2 WHERE id = ?1 AND status = 'candidate' AND verification_basis = 'unverified' AND NOT EXISTS (SELECT 1 FROM knowledge_sources s WHERE s.knowledge_id = knowledge_entries.id)", id, now)
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("confirming knowledge candidate: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("checking confirmed knowledge: %w", err)
	}
	if count != 1 {
		return KnowledgeRecord{}, errors.New("knowledge entry is not a confirmable candidate")
	}
	var topic, statement string
	if err := tx.QueryRow("SELECT topic, statement FROM knowledge_entries WHERE id = ?1", id).Scan(&topic, &statement); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("reading confirmed knowledge content: %w", err)
	}
	if err := enqueueKnowledgeEmbeddingJob(tx, id, topic, statement, now); err != nil {
		return KnowledgeRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("committing knowledge confirmation transaction: %w", err)
	}
	return knowledgeByID(db, id)
}

func (s *Store) TombstoneKnowledge(id string) error {
	if err := validateID("knowledge_id", id); err != nil {
		return err
	}
	db, err := s.openWrite()
	if err != nil {
		return err
	}
	defer db.Close()
	changed, err := db.Exec("UPDATE knowledge_entries SET status = 'tombstone', updated_at_ms = ?2 WHERE id = ?1 AND status IN ('candidate', 'verified')", id, nowUnixMS())
	if err != nil {
		return fmt.Errorf("tombstoning knowledge: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking tombstone knowledge: %w", err)
	}
	if count != 1 {
		return errors.New("knowledge entry is not tombstoneable")
	}
	return nil
}

func (s *Store) ExtractionBatchCatalog(characterID string) (ExtractionBatchCatalog, error) {
	if err := validateID("character_id", characterID); err != nil {
		return ExtractionBatchCatalog{}, err
	}
	db, err := s.openReadOnly()
	if err != nil {
		return ExtractionBatchCatalog{}, err
	}
	defer db.Close()
	running, err := listExtractionBatches(db, characterID, "running")
	if err != nil {
		return ExtractionBatchCatalog{}, err
	}
	failed, err := listExtractionBatches(db, characterID, "failed")
	if err != nil {
		return ExtractionBatchCatalog{}, err
	}
	return ExtractionBatchCatalog{Running: running, Failed: failed}, nil
}

func (s *Store) RetryExtractionBatch(id string) error {
	if err := validateID("batch_id", id); err != nil {
		return err
	}
	db, err := s.openWrite()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning extraction retry transaction: %w", err)
	}
	defer tx.Rollback()
	var conversationID string
	if err := tx.QueryRow("SELECT conversation_id FROM extraction_batches WHERE id = ?1 AND status = 'failed'", id).Scan(&conversationID); err != nil {
		return fmt.Errorf("reading failed extraction batch: %w", err)
	}
	now := nowUnixMS()
	if _, err := tx.Exec("UPDATE conversation_turns SET extraction_state = 'pending', updated_at_ms = ?2 WHERE id IN (SELECT turn_id FROM extraction_batch_turns WHERE batch_id = ?1) AND extraction_state = 'claimed'", id, now); err != nil {
		return fmt.Errorf("releasing extraction batch turns: %w", err)
	}
	changed, err := tx.Exec("UPDATE extraction_batches SET status = 'cancelled', updated_at_ms = ?2 WHERE id = ?1 AND status = 'failed'", id, now)
	if err != nil {
		return fmt.Errorf("cancelling failed extraction batch: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking extraction retry: %w", err)
	}
	if count != 1 || conversationID == "" {
		return errors.New("extraction batch is not retryable")
	}
	return tx.Commit()
}

func listKnowledge(db *sql.DB, status string) ([]KnowledgeRecord, error) {
	rows, err := db.Query("SELECT id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM knowledge_entries WHERE status = ?1 ORDER BY updated_at_ms DESC, id ASC LIMIT 20", status)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge catalog: %w", err)
	}
	defer rows.Close()
	records := make([]KnowledgeRecord, 0)
	for rows.Next() {
		record, err := scanKnowledge(db, rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func knowledgeByID(db *sql.DB, id string) (KnowledgeRecord, error) {
	row := db.QueryRow("SELECT id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM knowledge_entries WHERE id = ?1", id)
	return scanKnowledge(db, row)
}

type knowledgeScanner interface{ Scan(dest ...any) error }

func scanKnowledge(db *sql.DB, scanner knowledgeScanner) (KnowledgeRecord, error) {
	var record KnowledgeRecord
	var confidence int
	var supersedes sql.NullString
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
	sources, err := knowledgeSources(db, record.ID)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	record.Sources = sources
	return record, nil
}

type knowledgeSourceQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func knowledgeSources(db knowledgeSourceQuerier, id string) ([]AssistantSource, error) {
	rows, err := db.Query("SELECT title, url, snippet, rank, fetched_at_ms FROM knowledge_sources WHERE knowledge_id = ?1 ORDER BY rank ASC", id)
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
	return sources, rows.Err()
}

func listExtractionBatches(db *sql.DB, characterID string, status string) ([]ExtractionBatchRecord, error) {
	rows, err := db.Query("SELECT id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence, error_code, error_message, error_retryable, created_at_ms, updated_at_ms FROM extraction_batches WHERE character_id = ?1 AND status = ?2 ORDER BY updated_at_ms DESC, id ASC LIMIT 20", characterID, status)
	if err != nil {
		return nil, fmt.Errorf("querying extraction batches: %w", err)
	}
	defer rows.Close()
	records := make([]ExtractionBatchRecord, 0)
	for rows.Next() {
		var record ExtractionBatchRecord
		var first, last int64
		var code, message sql.NullString
		var retryable sql.NullInt64
		if err := rows.Scan(&record.ID, &record.ConversationID, &record.CharacterID, &record.Status, &first, &last, &code, &message, &retryable, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning extraction batch: %w", err)
		}
		record.FirstTurnSequence = uint64(first)
		record.LastTurnSequence = uint64(last)
		if code.Valid && message.Valid && retryable.Valid {
			record.Error = &WireError{Code: code.String, Message: message.String, Retryable: retryable.Int64 != 0}
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
