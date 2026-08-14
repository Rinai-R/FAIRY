package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) catalogSeekDB(ctx context.Context) (Catalog, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	candidates, err := listSeekDBKnowledge(queryCtx, s.seekDB, "candidate")
	if err != nil {
		return Catalog{}, err
	}
	verified, err := listSeekDBKnowledge(queryCtx, s.seekDB, "verified")
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Candidates: candidates, Verified: verified}, nil
}

func (s *Store) confirmCandidateSeekDB(
	ctx context.Context,
	id string,
	requireContext bool,
) (Record, error) {
	if err := validateSeekDBKnowledgeID("knowledge_id", id); err != nil {
		return Record{}, err
	}
	snapshotCtx, snapshotCancel := s.seekDBQueryContext(ctx)
	snapshot, err := seekDBKnowledgeByID(snapshotCtx, s.seekDB, id, false)
	snapshotCancel()
	if err != nil {
		return Record{}, err
	}
	if snapshot.Status != "candidate" || snapshot.VerificationBasis != "unverified" || len(snapshot.Sources) != 0 {
		return Record{}, errors.New("knowledge entry is not a confirmable candidate")
	}
	content := snapshot.Topic + "\n" + snapshot.Statement
	values, err := prepareKnowledgeEmbeddings(
		ctx, s.semanticEmbedderSnapshot(), []string{content}, requireContext,
	)
	if err != nil {
		return Record{}, err
	}
	spaceID, contentHash, vector, err := seekDBKnowledgeEmbeddingTuple(content, values[0])
	if err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(context.WithoutCancel(ctx))
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("beginning SeekDB knowledge confirmation transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockSeekDBKnowledgeSource(
		queryCtx, tx, snapshot.SourceConversationID, snapshot.SourceTurnID,
	); err != nil {
		return Record{}, err
	}
	current, err := seekDBKnowledgeByID(queryCtx, tx, id, true)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, errors.New("knowledge entry is not a confirmable candidate")
	}
	if err != nil {
		return Record{}, err
	}
	if !sameKnowledgeConfirmationSnapshot(current, snapshot) {
		return Record{}, errors.New("knowledge changed during confirmation")
	}
	effectiveNow := max(s.currentUnixMS(), current.UpdatedAtUnixMS)
	result, err := tx.ExecContext(queryCtx, `
UPDATE knowledge_entries
SET status = 'verified',
    verification_basis = 'user_confirmed',
    embedding_space_id = ?,
    embedding_content_hash = ?,
    embedding = ?,
    updated_at_ms = ?
WHERE id = ?
  AND status = 'candidate'
  AND verification_basis = 'unverified'
  AND source_url IS NULL`,
		spaceID, contentHash, vector, effectiveNow, id,
	)
	if err != nil {
		return Record{}, fmt.Errorf("confirming SeekDB knowledge candidate: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Record{}, fmt.Errorf("reading confirmed SeekDB knowledge count: %w", err)
	}
	if rows != 1 {
		return Record{}, errors.New("knowledge entry is not a confirmable candidate")
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing SeekDB knowledge confirmation transaction: %w", err)
	}
	return seekDBKnowledgeByID(queryCtx, s.seekDB, id, false)
}

func sameKnowledgeConfirmationSnapshot(current, snapshot Record) bool {
	return current.ID == snapshot.ID &&
		current.Topic == snapshot.Topic && current.Statement == snapshot.Statement &&
		current.Status == snapshot.Status && current.VerificationBasis == snapshot.VerificationBasis &&
		current.ConfidenceBasisPoints == snapshot.ConfidenceBasisPoints &&
		current.SourceConversationID == snapshot.SourceConversationID &&
		current.SourceTurnID == snapshot.SourceTurnID &&
		equalOptionalKnowledgeID(current.SupersedesID, snapshot.SupersedesID) &&
		len(current.Sources) == 0 && len(snapshot.Sources) == 0 &&
		current.CreatedAtUnixMS == snapshot.CreatedAtUnixMS &&
		current.UpdatedAtUnixMS == snapshot.UpdatedAtUnixMS
}

func equalOptionalKnowledgeID(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Store) tombstoneSeekDB(ctx context.Context, id string) error {
	if err := validateSeekDBKnowledgeID("knowledge_id", id); err != nil {
		return err
	}
	snapshotCtx, snapshotCancel := s.seekDBQueryContext(ctx)
	snapshot, err := seekDBKnowledgeByID(snapshotCtx, s.seekDB, id, false)
	snapshotCancel()
	if err != nil {
		return err
	}
	if snapshot.Status != "candidate" && snapshot.Status != "verified" {
		return errors.New("knowledge entry is not tombstoneable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	queryCtx, cancel := s.seekDBQueryContext(context.WithoutCancel(ctx))
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return fmt.Errorf("beginning SeekDB knowledge tombstone transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockSeekDBKnowledgeSource(
		queryCtx, tx, snapshot.SourceConversationID, snapshot.SourceTurnID,
	); err != nil {
		return err
	}
	current, err := seekDBKnowledgeByID(queryCtx, tx, id, true)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("knowledge entry is not tombstoneable")
	}
	if err != nil {
		return err
	}
	if current.Status != "candidate" && current.Status != "verified" {
		return errors.New("knowledge entry is not tombstoneable")
	}
	effectiveNow := max(s.currentUnixMS(), current.UpdatedAtUnixMS)
	result, err := tx.ExecContext(queryCtx, `
UPDATE knowledge_entries
SET status = 'tombstone', updated_at_ms = ?
WHERE id = ? AND status IN ('candidate', 'verified')`, effectiveNow, id)
	if err != nil {
		return fmt.Errorf("tombstoning SeekDB knowledge: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading tombstoned SeekDB knowledge count: %w", err)
	}
	if rows != 1 {
		return errors.New("knowledge entry is not tombstoneable")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing SeekDB knowledge tombstone transaction: %w", err)
	}
	return nil
}
