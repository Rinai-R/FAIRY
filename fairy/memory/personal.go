package memory

import (
	"database/sql"
	"errors"
	"fmt"
)

type MemoryScope struct {
	Type        string `json:"type"`
	CharacterID string `json:"characterId,omitempty"`
}

type PersonalMemoryRecord struct {
	ID                    string      `json:"id"`
	Kind                  string      `json:"kind"`
	Scope                 MemoryScope `json:"scope"`
	ReviewStatus          string      `json:"reviewStatus"`
	Content               string      `json:"content"`
	Status                string      `json:"status"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
	SourceConversationID  string      `json:"sourceConversationId"`
	SourceTurnID          string      `json:"sourceTurnId"`
	SupersedesID          *string     `json:"supersedesId"`
	CreatedAtUnixMS       int64       `json:"createdAtUnixMs"`
	UpdatedAtUnixMS       int64       `json:"updatedAtUnixMs"`
}

type PersonalMemoryCatalog struct {
	Global      []PersonalMemoryRecord `json:"global"`
	Character   []PersonalMemoryRecord `json:"character"`
	NeedsReview []PersonalMemoryRecord `json:"needsReview"`
}

func (s *Store) PersonalMemoryCatalog(characterID string) (PersonalMemoryCatalog, error) {
	if err := validateID("character_id", characterID); err != nil {
		return PersonalMemoryCatalog{}, err
	}
	db, err := s.openReadOnly()
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	defer db.Close()
	global, err := listPersonalMemories(db, "global", "", "ready")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	character, err := listPersonalMemories(db, "character", characterID, "ready")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	needsReview, err := listPersonalMemories(db, "unassigned_legacy", "", "needs_review")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	return PersonalMemoryCatalog{Global: global, Character: character, NeedsReview: needsReview}, nil
}

func (s *Store) CreatePersonalMemory(kind string, scope MemoryScope, content string, confidence uint16) (PersonalMemoryRecord, error) {
	if err := validateMemoryInput(kind, scope, content, confidence); err != nil {
		return PersonalMemoryRecord{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning memory transaction: %w", err)
	}
	defer tx.Rollback()
	sourceConversationID, sourceTurnID, err := latestMemorySource(tx, scope)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	record, err := insertPersonalMemory(tx, newID(), kind, scope, content, confidence, sourceConversationID, sourceTurnID, nil, now)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing memory transaction: %w", err)
	}
	return record, nil
}

func (s *Store) RevisePersonalMemory(id string, content string, confidence uint16) (PersonalMemoryRecord, error) {
	if err := validateID("memory_id", id); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := validateContent("memory content", content); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if confidence > 10000 {
		return PersonalMemoryRecord{}, errors.New("memory confidence is invalid")
	}
	db, err := s.openWrite()
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning memory revision transaction: %w", err)
	}
	defer tx.Rollback()
	current, err := personalMemoryByID(tx, id)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if current.Status != "active" {
		return PersonalMemoryRecord{}, errors.New("memory is not active")
	}
	if _, err := tx.Exec("UPDATE personal_memories SET status = 'superseded', updated_at_ms = ?2 WHERE id = ?1 AND status = 'active'", id, now); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("superseding memory: %w", err)
	}
	record, err := insertPersonalMemory(tx, newID(), current.Kind, current.Scope, content, confidence, current.SourceConversationID, current.SourceTurnID, &id, now)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing memory revision transaction: %w", err)
	}
	return record, nil
}

func (s *Store) TombstonePersonalMemory(id string) error {
	if err := validateID("memory_id", id); err != nil {
		return err
	}
	db, err := s.openWrite()
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec("UPDATE personal_memories SET status = 'tombstone', updated_at_ms = ?2 WHERE id = ?1 AND status = 'active'", id, nowUnixMS())
	if err != nil {
		return fmt.Errorf("tombstoning memory: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking tombstone result: %w", err)
	}
	if changed != 1 {
		return errors.New("active memory not found")
	}
	return nil
}

func (s *Store) AssignLegacyRelationship(id string, characterID string) (PersonalMemoryRecord, error) {
	if err := validateID("memory_id", id); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := validateID("character_id", characterID); err != nil {
		return PersonalMemoryRecord{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning legacy assignment transaction: %w", err)
	}
	defer tx.Rollback()
	current, err := personalMemoryByID(tx, id)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if current.Kind != "relationship" || current.Scope.Type != "unassigned_legacy" || current.Status != "active" {
		return PersonalMemoryRecord{}, errors.New("memory is not an active legacy relationship")
	}
	if _, err := tx.Exec("UPDATE personal_memories SET status = 'superseded', updated_at_ms = ?2 WHERE id = ?1 AND status = 'active'", id, now); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("superseding legacy memory: %w", err)
	}
	record, err := insertPersonalMemory(tx, newID(), current.Kind, MemoryScope{Type: "character", CharacterID: characterID}, current.Content, current.ConfidenceBasisPoints, current.SourceConversationID, current.SourceTurnID, &id, now)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing legacy assignment transaction: %w", err)
	}
	return record, nil
}

func listPersonalMemories(db *sql.DB, scopeKind string, characterID string, reviewStatus string) ([]PersonalMemoryRecord, error) {
	var rows *sql.Rows
	var err error
	if characterID == "" {
		rows, err = db.Query("SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE scope_kind = ?1 AND character_id IS NULL AND review_status = ?2 AND status = 'active' ORDER BY updated_at_ms DESC, id ASC LIMIT 100", scopeKind, reviewStatus)
	} else {
		rows, err = db.Query("SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE scope_kind = ?1 AND character_id = ?2 AND review_status = ?3 AND status = 'active' ORDER BY updated_at_ms DESC, id ASC LIMIT 100", scopeKind, characterID, reviewStatus)
	}
	if err != nil {
		return nil, fmt.Errorf("querying personal memories: %w", err)
	}
	defer rows.Close()
	return scanPersonalMemoryRows(rows)
}

func scanPersonalMemoryRows(rows *sql.Rows) ([]PersonalMemoryRecord, error) {
	records := make([]PersonalMemoryRecord, 0)
	for rows.Next() {
		record, err := scanPersonalMemory(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal memories: %w", err)
	}
	return records, nil
}

type memoryScanner interface {
	Scan(dest ...any) error
}

func scanPersonalMemory(scanner memoryScanner) (PersonalMemoryRecord, error) {
	var record PersonalMemoryRecord
	var scopeKind string
	var characterID sql.NullString
	var confidence int
	var supersedesID sql.NullString
	if err := scanner.Scan(&record.ID, &record.Kind, &scopeKind, &characterID, &record.ReviewStatus, &record.Content, &record.Status, &confidence, &record.SourceConversationID, &record.SourceTurnID, &supersedesID, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("scanning personal memory: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return PersonalMemoryRecord{}, errors.New("memory confidence is invalid")
	}
	record.ConfidenceBasisPoints = uint16(confidence)
	record.Scope = MemoryScope{Type: scopeKind}
	if characterID.Valid {
		record.Scope.CharacterID = characterID.String
	}
	if supersedesID.Valid {
		record.SupersedesID = &supersedesID.String
	}
	return record, nil
}

type personalMemoryReader interface {
	QueryRow(query string, args ...any) *sql.Row
}

func personalMemoryByID(db personalMemoryReader, id string) (PersonalMemoryRecord, error) {
	row := db.QueryRow("SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE id = ?1", id)
	return scanPersonalMemory(row)
}

func insertPersonalMemory(tx *sql.Tx, id string, kind string, scope MemoryScope, content string, confidence uint16, sourceConversationID string, sourceTurnID string, supersedesID *string, now int64) (PersonalMemoryRecord, error) {
	scopeKind, characterID, reviewStatus := memoryScopeColumns(scope)
	if _, err := tx.Exec("INSERT INTO personal_memories(id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'active', ?7, ?8, ?9, ?10, ?11, ?11)", id, kind, scopeKind, characterID, reviewStatus, content, int(confidence), sourceConversationID, sourceTurnID, supersedesID, now); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("inserting personal memory: %w", err)
	}
	if reviewStatus == "ready" {
		if err := enqueuePersonalMemoryEmbeddingJob(tx, id, content, now); err != nil {
			return PersonalMemoryRecord{}, err
		}
	}
	return PersonalMemoryRecord{ID: id, Kind: kind, Scope: scope, ReviewStatus: reviewStatus, Content: content, Status: "active", ConfidenceBasisPoints: confidence, SourceConversationID: sourceConversationID, SourceTurnID: sourceTurnID, SupersedesID: supersedesID, CreatedAtUnixMS: now, UpdatedAtUnixMS: now}, nil
}

func latestMemorySource(tx *sql.Tx, scope MemoryScope) (string, string, error) {
	var conversationID, turnID string
	var err error
	if scope.Type == "character" {
		err = tx.QueryRow("SELECT c.id, t.id FROM conversations c JOIN conversation_turns t ON t.conversation_id = c.id WHERE c.character_id = ?1 ORDER BY c.updated_at_ms DESC, t.sequence DESC LIMIT 1", scope.CharacterID).Scan(&conversationID, &turnID)
	} else {
		err = tx.QueryRow("SELECT c.id, t.id FROM conversations c JOIN conversation_turns t ON t.conversation_id = c.id ORDER BY c.updated_at_ms DESC, t.sequence DESC LIMIT 1").Scan(&conversationID, &turnID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("memory write requires an existing conversation turn")
	}
	if err != nil {
		return "", "", fmt.Errorf("reading memory source turn: %w", err)
	}
	return conversationID, turnID, nil
}

func validateMemoryInput(kind string, scope MemoryScope, content string, confidence uint16) error {
	if kind != "preference" && kind != "profile" && kind != "relationship" && kind != "experience" {
		return errors.New("memory kind is unsupported")
	}
	if kind == "relationship" {
		if scope.Type != "character" && scope.Type != "unassigned_legacy" {
			return errors.New("relationship memory requires character or legacy scope")
		}
	} else if scope.Type != "global" {
		return errors.New("non-relationship memory requires global scope")
	}
	if scope.Type == "character" {
		if err := validateID("character_id", scope.CharacterID); err != nil {
			return err
		}
	}
	if err := validateContent("memory content", content); err != nil {
		return err
	}
	if confidence > 10000 {
		return errors.New("memory confidence is invalid")
	}
	return nil
}

func memoryScopeColumns(scope MemoryScope) (string, *string, string) {
	if scope.Type == "character" {
		return "character", &scope.CharacterID, "ready"
	}
	if scope.Type == "unassigned_legacy" {
		return "unassigned_legacy", nil, "needs_review"
	}
	return "global", nil, "ready"
}
