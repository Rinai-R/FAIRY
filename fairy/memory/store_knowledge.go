package memory

import (
	"context"
	"errors"
	"fmt"
)

func (s *Store) knowledgeCatalogPostgres(ctx context.Context) (KnowledgeCatalog, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	candidates, err := ListKnowledge(queryCtx, s.pool.Raw(), "candidate")
	if err != nil {
		return KnowledgeCatalog{}, err
	}
	verified, err := ListKnowledge(queryCtx, s.pool.Raw(), "verified")
	if err != nil {
		return KnowledgeCatalog{}, err
	}
	return KnowledgeCatalog{Candidates: candidates, Verified: verified}, nil
}

func (s *Store) confirmKnowledgeCandidatePostgres(ctx context.Context, id string) (KnowledgeRecord, error) {
	if err := ValidateID("knowledge_id", id); err != nil {
		return KnowledgeRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	snapshot, err := knowledgeByIDPostgres(queryCtx, s.pool.Raw(), id)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	if snapshot.Status != "candidate" || snapshot.VerificationBasis != "unverified" || len(snapshot.Sources) != 0 {
		return KnowledgeRecord{}, errors.New("knowledge entry is not a confirmable candidate")
	}
	embedding, err := s.embeddingForContent(snapshot.Topic + "\n" + snapshot.Statement)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("beginning knowledge confirmation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	topic, statement, err := ConfirmKnowledgeCandidate(queryCtx, tx, id, now, embedding)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	if topic != snapshot.Topic || statement != snapshot.Statement {
		return KnowledgeRecord{}, errors.New("knowledge changed during confirmation")
	}
	if err := tx.Commit(queryCtx); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("committing knowledge confirmation transaction: %w", err)
	}
	return knowledgeByIDPostgres(ctx, s.pool.Raw(), id)
}

func (s *Store) tombstoneKnowledgePostgres(ctx context.Context, id string) error {
	if err := ValidateID("knowledge_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return TombstoneKnowledge(queryCtx, s.pool.Raw(), id, nowUnixMS())
}

func (s *Store) extractionBatchCatalogPostgres(ctx context.Context, characterID string) (ExtractionBatchCatalog, error) {
	if err := ValidateID("character_id", characterID); err != nil {
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
	if err := ValidateID("batch_id", id); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning extraction retry transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if _, err := RetryFailedExtractionBatch(queryCtx, tx, id, nowUnixMS()); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing extraction retry: %w", err)
	}
	return nil
}

func listExtractionBatchesPostgres(ctx context.Context, db Querier, characterID, status string) ([]ExtractionBatchRecord, error) {
	return ListExtractionBatches(ctx, db, characterID, status)
}

func knowledgeByIDPostgres(ctx context.Context, db ConversationDB, id string) (KnowledgeRecord, error) {
	return KnowledgeByID(ctx, db, id)
}

func knowledgeSourcesPostgres(ctx context.Context, db Querier, id string) ([]AssistantSource, error) {
	return KnowledgeSources(ctx, db, id)
}
