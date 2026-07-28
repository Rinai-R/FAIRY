package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Store) personalMemoryCatalogPostgres(ctx context.Context, characterID string) (PersonalMemoryCatalog, error) {
	if err := ValidateID("character_id", characterID); err != nil {
		return PersonalMemoryCatalog{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	global, err := ListPersonalMemories(queryCtx, s.pool.Raw(), "global", "", "ready")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	character, err := ListPersonalMemories(queryCtx, s.pool.Raw(), "character", characterID, "ready")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	needsReview, err := ListPersonalMemories(queryCtx, s.pool.Raw(), "unassigned_legacy", "", "needs_review")
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	return PersonalMemoryCatalog{Global: global, Character: character, NeedsReview: needsReview}, nil
}

func (s *Store) createPersonalMemoryPostgres(ctx context.Context, kind string, scope MemoryScope, content string, confidence uint16) (PersonalMemoryRecord, error) {
	if err := ValidateMemoryInput(kind, scope, content, confidence); err != nil {
		return PersonalMemoryRecord{}, err
	}
	var embedding EmbeddingValue
	if scope.Type != "unassigned_legacy" {
		var err error
		embedding, err = s.embeddingForContent(content)
		if err != nil {
			return PersonalMemoryRecord{}, err
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning memory transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	conversationID, turnID, err := LatestMemorySource(queryCtx, tx, scope)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	record, err := InsertPersonalMemory(queryCtx, tx, newID(), kind, scope, content, confidence, conversationID, turnID, nil, nowUnixMS(), embedding)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing memory transaction: %w", err)
	}
	return record, nil
}

func (s *Store) revisePersonalMemoryPostgres(ctx context.Context, id, content string, confidence uint16) (PersonalMemoryRecord, error) {
	if err := ValidateID("memory_id", id); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := ValidatePersonalMemoryContent(content); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if confidence > 10000 {
		return PersonalMemoryRecord{}, errors.New("memory confidence is invalid")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	snapshot, err := PersonalMemoryByID(queryCtx, s.pool.Raw(), id, false)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if snapshot.Status != "active" {
		return PersonalMemoryRecord{}, errors.New("memory is not active")
	}
	var embedding EmbeddingValue
	if snapshot.ReviewStatus == "ready" {
		embedding, err = s.embeddingForContent(content)
		if err != nil {
			return PersonalMemoryRecord{}, err
		}
	}
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning memory revision transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	current, err := PersonalMemoryByID(queryCtx, tx, id, true)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if current.Status != "active" {
		return PersonalMemoryRecord{}, errors.New("memory is not active")
	}
	if current.Kind != snapshot.Kind || current.Scope != snapshot.Scope || current.ReviewStatus != snapshot.ReviewStatus {
		return PersonalMemoryRecord{}, errors.New("memory changed during revision")
	}
	now := nowUnixMS()
	changed, err := tx.Exec(queryCtx, "UPDATE personal_memories SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", id, now)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("superseding memory: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return PersonalMemoryRecord{}, errors.New("memory is not active")
	}
	record, err := InsertPersonalMemory(queryCtx, tx, newID(), current.Kind, current.Scope, content, confidence, current.SourceConversationID, current.SourceTurnID, &id, now, embedding)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing memory revision transaction: %w", err)
	}
	return record, nil
}

func (s *Store) tombstonePersonalMemoryPostgres(ctx context.Context, id string) error {
	if err := ValidateID("memory_id", id); err != nil {
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
	if err := ValidateID("memory_id", id); err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := ValidateID("character_id", characterID); err != nil {
		return PersonalMemoryRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	snapshot, err := PersonalMemoryByID(queryCtx, s.pool.Raw(), id, false)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if snapshot.Kind != "relationship" || snapshot.Scope.Type != "unassigned_legacy" || snapshot.Status != "active" {
		return PersonalMemoryRecord{}, errors.New("memory is not an active legacy relationship")
	}
	if err := ValidatePersonalMemoryContent(snapshot.Content); err != nil {
		return PersonalMemoryRecord{}, err
	}
	embedding, err := s.embeddingForContent(snapshot.Content)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("beginning legacy assignment transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	current, err := PersonalMemoryByID(queryCtx, tx, id, true)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if current.Kind != "relationship" || current.Scope.Type != "unassigned_legacy" || current.Status != "active" {
		return PersonalMemoryRecord{}, errors.New("memory is not an active legacy relationship")
	}
	if current.Content != snapshot.Content {
		return PersonalMemoryRecord{}, errors.New("memory changed during legacy assignment")
	}
	if err := ValidatePersonalMemoryContent(current.Content); err != nil {
		return PersonalMemoryRecord{}, err
	}
	now := nowUnixMS()
	changed, err := tx.Exec(queryCtx, "UPDATE personal_memories SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", id, now)
	if err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("superseding legacy memory: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return PersonalMemoryRecord{}, errors.New("memory is not an active legacy relationship")
	}
	record, err := InsertPersonalMemory(queryCtx, tx, newID(), current.Kind, MemoryScope{Type: "character", CharacterID: characterID}, current.Content, current.ConfidenceBasisPoints, current.SourceConversationID, current.SourceTurnID, &id, now, embedding)
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersonalMemoryRecord{}, fmt.Errorf("committing legacy assignment transaction: %w", err)
	}
	return record, nil
}

func insertPersonalMemoryPostgres(ctx context.Context, tx pgx.Tx, id, kind string, scope MemoryScope, content string, confidence uint16, sourceConversationID, sourceTurnID string, supersedesID *string, now int64, embedding EmbeddingValue) (PersonalMemoryRecord, error) {
	return InsertPersonalMemory(ctx, tx, id, kind, scope, content, confidence, sourceConversationID, sourceTurnID, supersedesID, now, embedding)
}
