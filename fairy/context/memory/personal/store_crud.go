package personal

import (
	"context"
	"errors"
	"fairy/runtime/embedding"
	"fmt"
)

func (s *Store) personalMemoryCatalogPostgres(ctx context.Context, characterID string) (Catalog, error) {
	if err := validateID("character_id", characterID); err != nil {
		return Catalog{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	global, err := List(queryCtx, s.pool.Raw(), "global", "", "ready")
	if err != nil {
		return Catalog{}, err
	}
	character, err := List(queryCtx, s.pool.Raw(), "character", characterID, "ready")
	if err != nil {
		return Catalog{}, err
	}
	needsReview, err := List(queryCtx, s.pool.Raw(), "unassigned_legacy", "", "needs_review")
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Global: global, Character: character, NeedsReview: needsReview}, nil
}

func (s *Store) createPersonalMemoryPostgres(ctx context.Context, kind string, scope Scope, content string, confidence uint16) (Record, error) {
	if err := ValidateInput(kind, scope, content, confidence); err != nil {
		return Record{}, err
	}
	var embedding embedding.EmbeddingValue
	if scope.Type != "unassigned_legacy" {
		var err error
		embedding, err = s.embeddingForContent(content)
		if err != nil {
			return Record{}, err
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Record{}, fmt.Errorf("beginning memory transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	conversationID, turnID, err := LatestSource(queryCtx, tx, scope)
	if err != nil {
		return Record{}, err
	}
	record, err := Insert(queryCtx, tx, newID(), kind, scope, content, confidence, conversationID, turnID, nil, nowUnixMS(), embedding)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Record{}, fmt.Errorf("committing memory transaction: %w", err)
	}
	return record, nil
}

func (s *Store) revisePersonalMemoryPostgres(ctx context.Context, id, content string, confidence uint16) (Record, error) {
	if err := validateID("memory_id", id); err != nil {
		return Record{}, err
	}
	if err := ValidateContent(content); err != nil {
		return Record{}, err
	}
	if confidence > 10000 {
		return Record{}, errors.New("memory confidence is invalid")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	snapshot, err := ByID(queryCtx, s.pool.Raw(), id, false)
	if err != nil {
		return Record{}, err
	}
	if snapshot.Status != "active" {
		return Record{}, errors.New("memory is not active")
	}
	var embedding embedding.EmbeddingValue
	if snapshot.ReviewStatus == "ready" {
		embedding, err = s.embeddingForContent(content)
		if err != nil {
			return Record{}, err
		}
	}
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Record{}, fmt.Errorf("beginning memory revision transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	current, err := ByID(queryCtx, tx, id, true)
	if err != nil {
		return Record{}, err
	}
	if current.Status != "active" {
		return Record{}, errors.New("memory is not active")
	}
	if current.Kind != snapshot.Kind || current.Scope != snapshot.Scope || current.ReviewStatus != snapshot.ReviewStatus {
		return Record{}, errors.New("memory changed during revision")
	}
	now := nowUnixMS()
	changed, err := tx.Exec(queryCtx, "UPDATE personal_memories SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", id, now)
	if err != nil {
		return Record{}, fmt.Errorf("superseding memory: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return Record{}, errors.New("memory is not active")
	}
	record, err := Insert(queryCtx, tx, newID(), current.Kind, current.Scope, content, confidence, current.SourceConversationID, current.SourceTurnID, &id, now, embedding)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Record{}, fmt.Errorf("committing memory revision transaction: %w", err)
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

func (s *Store) assignLegacyRelationshipPostgres(ctx context.Context, id, characterID string) (Record, error) {
	if err := validateID("memory_id", id); err != nil {
		return Record{}, err
	}
	if err := validateID("character_id", characterID); err != nil {
		return Record{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	snapshot, err := ByID(queryCtx, s.pool.Raw(), id, false)
	if err != nil {
		return Record{}, err
	}
	if snapshot.Kind != "relationship" || snapshot.Scope.Type != "unassigned_legacy" || snapshot.Status != "active" {
		return Record{}, errors.New("memory is not an active legacy relationship")
	}
	if err := ValidateContent(snapshot.Content); err != nil {
		return Record{}, err
	}
	embedding, err := s.embeddingForContent(snapshot.Content)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Record{}, fmt.Errorf("beginning legacy assignment transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	current, err := ByID(queryCtx, tx, id, true)
	if err != nil {
		return Record{}, err
	}
	if current.Kind != "relationship" || current.Scope.Type != "unassigned_legacy" || current.Status != "active" {
		return Record{}, errors.New("memory is not an active legacy relationship")
	}
	if current.Content != snapshot.Content {
		return Record{}, errors.New("memory changed during legacy assignment")
	}
	if err := ValidateContent(current.Content); err != nil {
		return Record{}, err
	}
	now := nowUnixMS()
	changed, err := tx.Exec(queryCtx, "UPDATE personal_memories SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", id, now)
	if err != nil {
		return Record{}, fmt.Errorf("superseding legacy memory: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return Record{}, errors.New("memory is not an active legacy relationship")
	}
	record, err := Insert(queryCtx, tx, newID(), current.Kind, Scope{Type: "character", CharacterID: characterID}, current.Content, current.ConfidenceBasisPoints, current.SourceConversationID, current.SourceTurnID, &id, now, embedding)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Record{}, fmt.Errorf("committing legacy assignment transaction: %w", err)
	}
	return record, nil
}
