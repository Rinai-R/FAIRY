package extraction

import (
	"context"
	"fmt"
)

func (s *Store) extractionBatchCatalogPostgres(ctx context.Context, characterID string) (Catalog, error) {
	if err := validateID("character_id", characterID); err != nil {
		return Catalog{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	running, err := ListBatches(queryCtx, s.pool.Raw(), characterID, "running")
	if err != nil {
		return Catalog{}, err
	}
	failed, err := ListBatches(queryCtx, s.pool.Raw(), characterID, "failed")
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Running: running, Failed: failed}, nil
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
	if _, err := RetryFailedBatch(queryCtx, tx, id, nowUnixMS()); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing extraction retry: %w", err)
	}
	return nil
}
