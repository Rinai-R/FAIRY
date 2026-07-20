package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type memoryScanner interface {
	Scan(dest ...any) error
}

func (s *Store) personalMemoryCatalogPostgres(ctx context.Context, characterID string) (PersonalMemoryCatalog, error) {
	if err := validateID("character_id", characterID); err != nil {
		return PersonalMemoryCatalog{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	global, err := listPersonalMemoriesPostgres(queryCtx, s.pool.Raw(), "global", "", "ready")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	character, err := listPersonalMemoriesPostgres(queryCtx, s.pool.Raw(), "character", characterID, "ready")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	needsReview, err := listPersonalMemoriesPostgres(queryCtx, s.pool.Raw(), "unassigned_legacy", "", "needs_review")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	return PersonalMemoryCatalog{Global: global, Character: character, NeedsReview: needsReview}, nil
}

func (s *Store) createPersonalMemoryPostgres(ctx context.Context, kind string, scope MemoryScope, content string, confidence uint16) (PersonalMemoryRecord, error) {
	if err := validateMemoryInput(kind, scope, content, confidence); err != nil {
		return PersonalMemoryRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning memory transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	conversationID, turnID, err := latestMemorySourcePostgres(queryCtx, tx, scope)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	record, err := insertPersonalMemoryPostgres(queryCtx, tx, newID(), kind, scope, content, confidence, conversationID, turnID, nil, nowUnixMS())
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing memory transaction: %w", err)
	}
	return record, nil
}

func (s *Store) revisePersonalMemoryPostgres(ctx context.Context, id, content string, confidence uint16) (PersonalMemoryRecord, error) {
	if err := validateID("memory_id", id); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := validateContent("memory content", content); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if confidence > 10000 {
		return PersonalMemoryRecord{}, errors.New("memory confidence is invalid")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning memory revision transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	current, err := personalMemoryByIDPostgres(queryCtx, tx, id, true)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if current.Status != "active" {
		return PersonalMemoryRecord{}, errors.New("memory is not active")
	}
	now := nowUnixMS()
	changed, err := tx.Exec(queryCtx, "UPDATE personal_memories SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", id, now)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("superseding memory: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return PersonalMemoryRecord{}, errors.New("memory is not active")
	}
	record, err := insertPersonalMemoryPostgres(queryCtx, tx, newID(), current.Kind, current.Scope, content, confidence, current.SourceConversationID, current.SourceTurnID, &id, now)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing memory revision transaction: %w", err)
	}
	return record, nil
}

func (s *Store) tombstonePersonalMemoryPostgres(ctx context.Context, id string) error {
	if err := validateID("memory_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, "UPDATE personal_memories SET status = 'tombstone', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", id, nowUnixMS())
	if err != nil {
		return fmt.Errorf("tombstoning memory: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("active memory not found")
	}
	return nil
}

func (s *Store) assignLegacyRelationshipPostgres(ctx context.Context, id, characterID string) (PersonalMemoryRecord, error) {
	if err := validateID("memory_id", id); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := validateID("character_id", characterID); err != nil {
		return PersonalMemoryRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning legacy assignment transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	current, err := personalMemoryByIDPostgres(queryCtx, tx, id, true)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if current.Kind != "relationship" || current.Scope.Type != "unassigned_legacy" || current.Status != "active" {
		return PersonalMemoryRecord{}, errors.New("memory is not an active legacy relationship")
	}
	now := nowUnixMS()
	changed, err := tx.Exec(queryCtx, "UPDATE personal_memories SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", id, now)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("superseding legacy memory: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return PersonalMemoryRecord{}, errors.New("memory is not an active legacy relationship")
	}
	record, err := insertPersonalMemoryPostgres(queryCtx, tx, newID(), current.Kind, MemoryScope{Type: "character", CharacterID: characterID}, current.Content, current.ConfidenceBasisPoints, current.SourceConversationID, current.SourceTurnID, &id, now)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing legacy assignment transaction: %w", err)
	}
	return record, nil
}

type postgresQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func listPersonalMemoriesPostgres(ctx context.Context, db postgresQuerier, scopeKind, characterID, reviewStatus string) ([]PersonalMemoryRecord, error) {
	var rows pgx.Rows
	var err error
	if characterID == "" {
		rows, err = db.Query(ctx, "SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE scope_kind = $1 AND character_id IS NULL AND review_status = $2 AND status = 'active' ORDER BY updated_at_ms DESC, id ASC LIMIT 100", scopeKind, reviewStatus)
	} else {
		rows, err = db.Query(ctx, "SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE scope_kind = $1 AND character_id = $2 AND review_status = $3 AND status = 'active' ORDER BY updated_at_ms DESC, id ASC LIMIT 100", scopeKind, characterID, reviewStatus)
	}
	if err != nil {
		return nil, fmt.Errorf("querying personal memories: %w", err)
	}
	defer rows.Close()
	records := make([]PersonalMemoryRecord, 0)
	for rows.Next() {
		record, err := scanPersonalMemoryPostgres(rows)
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

func personalMemoryByIDPostgres(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (PersonalMemoryRecord, error) {
	query := "SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE id = $1"
	if forUpdate {
		query += " FOR UPDATE"
	}
	return scanPersonalMemoryPostgres(tx.QueryRow(ctx, query, id))
}

func scanPersonalMemoryPostgres(scanner memoryScanner) (PersonalMemoryRecord, error) {
	var record PersonalMemoryRecord
	var scopeKind string
	var characterID pgtype.Text
	var confidence int
	var supersedesID pgtype.Text
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

func insertPersonalMemoryPostgres(ctx context.Context, tx pgx.Tx, id, kind string, scope MemoryScope, content string, confidence uint16, sourceConversationID, sourceTurnID string, supersedesID *string, now int64) (PersonalMemoryRecord, error) {
	scopeKind, characterID, reviewStatus := memoryScopeColumns(scope)
	_, err := tx.Exec(ctx, "INSERT INTO personal_memories(id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, $4, $5, $6, 'active', $7, $8, $9, $10, $11, $11)", id, kind, scopeKind, characterID, reviewStatus, content, int(confidence), sourceConversationID, sourceTurnID, supersedesID, now)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("inserting personal memory: %w", err)
	}
	if reviewStatus == "ready" {
		if err := enqueuePersonalMemoryEmbeddingJobPostgres(ctx, tx, id, content, now); err != nil {
			return PersonalMemoryRecord{}, err
		}
	}
	return PersonalMemoryRecord{ID: id, Kind: kind, Scope: scope, ReviewStatus: reviewStatus, Content: content, Status: "active", ConfidenceBasisPoints: confidence, SourceConversationID: sourceConversationID, SourceTurnID: sourceTurnID, SupersedesID: supersedesID, CreatedAtUnixMS: now, UpdatedAtUnixMS: now}, nil
}

func latestMemorySourcePostgres(ctx context.Context, tx pgx.Tx, scope MemoryScope) (string, string, error) {
	var conversationID, turnID string
	var err error
	if scope.Type == "character" {
		err = tx.QueryRow(ctx, "SELECT c.id, t.id FROM conversations c JOIN conversation_turns t ON t.conversation_id = c.id WHERE c.character_id = $1 ORDER BY c.updated_at_ms DESC, t.sequence DESC LIMIT 1", scope.CharacterID).Scan(&conversationID, &turnID)
	} else {
		err = tx.QueryRow(ctx, "SELECT c.id, t.id FROM conversations c JOIN conversation_turns t ON t.conversation_id = c.id ORDER BY c.updated_at_ms DESC, t.sequence DESC LIMIT 1").Scan(&conversationID, &turnID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errors.New("memory write requires an existing conversation turn")
	}
	if err != nil {
		return "", "", fmt.Errorf("reading memory source turn: %w", err)
	}
	return conversationID, turnID, nil
}
