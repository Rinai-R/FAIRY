package personal

import (
	"context"
	"errors"
	"fmt"

	"fairy/runtime/embedding"
)

func (s *Store) personalMemoryCatalogSeekDB(ctx context.Context, characterID string) (Catalog, error) {
	if err := validateSeekDBID("character_id", characterID); err != nil {
		return Catalog{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	global, err := listSeekDBRecords(queryCtx, s.seekDB, "global", "", "ready")
	if err != nil {
		return Catalog{}, err
	}
	character, err := listSeekDBRecords(queryCtx, s.seekDB, "character", characterID, "ready")
	if err != nil {
		return Catalog{}, err
	}
	needsReview, err := listSeekDBRecords(queryCtx, s.seekDB, "unassigned_legacy", "", "needs_review")
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Global: global, Character: character, NeedsReview: needsReview}, nil
}

func (s *Store) createPersonalMemorySeekDB(
	ctx context.Context,
	kind string,
	scope Scope,
	content string,
	confidence uint16,
) (Record, error) {
	if err := ValidateInput(kind, scope, content, confidence); err != nil {
		return Record{}, err
	}
	if err := validateSeekDBScope(scope); err != nil {
		return Record{}, err
	}
	prepared := embedding.EmbeddingValue{}
	if scope.Type != "unassigned_legacy" {
		var err error
		prepared, err = s.PrepareEmbedding(content)
		if err != nil {
			return Record{}, err
		}
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("beginning SeekDB memory transaction: %w", err)
	}
	defer tx.Rollback()
	conversationID, turnID, err := latestSourceSeekDB(queryCtx, tx, scope)
	if err != nil {
		return Record{}, err
	}
	record, err := s.InsertSeekDBTx(
		queryCtx, tx, newID(), kind, scope, content, confidence,
		conversationID, turnID, nil, s.currentUnixMS(), prepared,
	)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing SeekDB memory transaction: %w", err)
	}
	return record, nil
}

func (s *Store) revisePersonalMemorySeekDB(
	ctx context.Context,
	id, content string,
	confidence uint16,
) (Record, error) {
	if err := validateSeekDBID("memory_id", id); err != nil {
		return Record{}, err
	}
	if err := ValidateContent(content); err != nil {
		return Record{}, err
	}
	if confidence > 10000 {
		return Record{}, errors.New("memory confidence is invalid")
	}
	snapshotCtx, snapshotCancel := s.seekDBQueryContext(ctx)
	snapshot, err := selectSeekDBRecord(snapshotCtx, s.seekDB, id, false)
	snapshotCancel()
	if err != nil {
		return Record{}, err
	}
	if snapshot.Status != "active" {
		return Record{}, errors.New("memory is not active")
	}
	prepared := embedding.EmbeddingValue{}
	if snapshot.ReviewStatus == "ready" {
		prepared, err = s.PrepareEmbedding(content)
		if err != nil {
			return Record{}, err
		}
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("beginning SeekDB memory revision transaction: %w", err)
	}
	defer tx.Rollback()
	current, err := selectSeekDBRecord(queryCtx, tx, id, true)
	if err != nil {
		return Record{}, err
	}
	if current.Status != "active" {
		return Record{}, errors.New("memory is not active")
	}
	if current.Kind != snapshot.Kind || current.Scope != snapshot.Scope ||
		current.ReviewStatus != snapshot.ReviewStatus || current.Content != snapshot.Content {
		return Record{}, errors.New("memory changed during revision")
	}
	now := s.currentUnixMS()
	if err := s.SupersedeSeekDBTx(queryCtx, tx, id, now); err != nil {
		return Record{}, err
	}
	record, err := s.InsertSeekDBTx(
		queryCtx, tx, newID(), current.Kind, current.Scope, content, confidence,
		current.SourceConversationID, current.SourceTurnID, &id, now, prepared,
	)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing SeekDB memory revision transaction: %w", err)
	}
	return record, nil
}

func (s *Store) tombstonePersonalMemorySeekDB(ctx context.Context, id string) error {
	if err := validateSeekDBID("memory_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	result, err := s.seekDB.ExecContext(queryCtx, `
UPDATE personal_memories
SET status = 'tombstone', updated_at_ms = ?
WHERE id = ? AND status = 'active'`, s.currentUnixMS(), id)
	if err != nil {
		return fmt.Errorf("tombstoning SeekDB memory: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading tombstoned SeekDB memory count: %w", err)
	}
	if rows != 1 {
		return errors.New("active memory not found")
	}
	return nil
}

func (s *Store) assignLegacyRelationshipSeekDB(ctx context.Context, id, characterID string) (Record, error) {
	if err := validateSeekDBID("memory_id", id); err != nil {
		return Record{}, err
	}
	if err := validateSeekDBID("character_id", characterID); err != nil {
		return Record{}, err
	}
	snapshotCtx, snapshotCancel := s.seekDBQueryContext(ctx)
	snapshot, err := selectSeekDBRecord(snapshotCtx, s.seekDB, id, false)
	snapshotCancel()
	if err != nil {
		return Record{}, err
	}
	if snapshot.Kind != "relationship" || snapshot.Scope.Type != "unassigned_legacy" || snapshot.Status != "active" {
		return Record{}, errors.New("memory is not an active legacy relationship")
	}
	if err := ValidateContent(snapshot.Content); err != nil {
		return Record{}, err
	}
	prepared, err := s.PrepareEmbedding(snapshot.Content)
	if err != nil {
		return Record{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("beginning SeekDB legacy assignment transaction: %w", err)
	}
	defer tx.Rollback()
	current, err := selectSeekDBRecord(queryCtx, tx, id, true)
	if err != nil {
		return Record{}, err
	}
	if current.Kind != "relationship" || current.Scope.Type != "unassigned_legacy" || current.Status != "active" {
		return Record{}, errors.New("memory is not an active legacy relationship")
	}
	if current.Content != snapshot.Content {
		return Record{}, errors.New("memory changed during legacy assignment")
	}
	now := s.currentUnixMS()
	if err := s.SupersedeSeekDBTx(queryCtx, tx, id, now); err != nil {
		return Record{}, err
	}
	record, err := s.InsertSeekDBTx(
		queryCtx, tx, newID(), current.Kind,
		Scope{Type: "character", CharacterID: characterID}, current.Content,
		current.ConfidenceBasisPoints, current.SourceConversationID, current.SourceTurnID,
		&id, now, prepared,
	)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing SeekDB legacy assignment transaction: %w", err)
	}
	return record, nil
}
