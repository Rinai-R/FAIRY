package knowledge

import (
	"context"
	"errors"
	"fmt"

	"fairy/runtime/embedding"
)

func (s *Store) Catalog() (Catalog, error) {
	return s.CatalogContext(context.Background())
}

func (s *Store) CatalogContext(ctx context.Context) (Catalog, error) {
	if s.usesSeekDB() {
		return s.catalogSeekDB(ctx)
	}
	if !s.usesPostgres() {
		return Catalog{}, ErrStoreBackendUnavailable
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	candidates, err := ListKnowledge(queryCtx, s.pool.Raw(), "candidate")
	if err != nil {
		return Catalog{}, err
	}
	verified, err := ListKnowledge(queryCtx, s.pool.Raw(), "verified")
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Candidates: candidates, Verified: verified}, nil
}

func (s *Store) ConfirmCandidate(id string) (Record, error) {
	return s.confirmCandidate(context.Background(), id, false)
}

func (s *Store) ConfirmCandidateContext(ctx context.Context, id string) (Record, error) {
	return s.confirmCandidate(ctx, id, true)
}

func (s *Store) confirmCandidate(ctx context.Context, id string, requireContext bool) (Record, error) {
	if s.usesSeekDB() {
		return s.confirmCandidateSeekDB(ctx, id, requireContext)
	}
	if !s.usesPostgres() {
		return Record{}, ErrStoreBackendUnavailable
	}
	_ = requireContext
	if err := ValidateID("knowledge_id", id); err != nil {
		return Record{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	snapshot, err := KnowledgeByID(queryCtx, s.pool.Raw(), id)
	if err != nil {
		return Record{}, err
	}
	if snapshot.Status != "candidate" || snapshot.VerificationBasis != "unverified" || len(snapshot.Sources) != 0 {
		return Record{}, errors.New("knowledge entry is not a confirmable candidate")
	}
	value, err := embedding.ForContent(s.embedder, snapshot.Topic+"\n"+snapshot.Statement)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Record{}, fmt.Errorf("beginning knowledge confirmation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	topic, statement, err := ConfirmKnowledgeCandidate(queryCtx, tx, id, nowUnixMS(), value)
	if err != nil {
		return Record{}, err
	}
	if topic != snapshot.Topic || statement != snapshot.Statement {
		return Record{}, errors.New("knowledge changed during confirmation")
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Record{}, fmt.Errorf("committing knowledge confirmation transaction: %w", err)
	}
	return KnowledgeByID(ctx, s.pool.Raw(), id)
}

func (s *Store) Tombstone(id string) error {
	return s.TombstoneContext(context.Background(), id)
}

func (s *Store) TombstoneContext(ctx context.Context, id string) error {
	if s.usesSeekDB() {
		return s.tombstoneSeekDB(ctx, id)
	}
	if !s.usesPostgres() {
		return ErrStoreBackendUnavailable
	}
	if err := ValidateID("knowledge_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return TombstoneKnowledge(queryCtx, s.pool.Raw(), id, nowUnixMS())
}
