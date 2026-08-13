package extraction

import "context"

func (s *Store) ExtractionBatchCatalog(characterID string) (Catalog, error) {
	return s.ExtractionBatchCatalogContext(context.Background(), characterID)
}

func (s *Store) ExtractionBatchCatalogContext(ctx context.Context, characterID string) (Catalog, error) {
	if s.usesSeekDB() {
		return s.extractionBatchCatalogSeekDB(ctx, characterID)
	}
	if !s.usesPostgres() {
		return Catalog{}, ErrStoreBackendUnavailable
	}
	return s.extractionBatchCatalogPostgres(ctx, characterID)
}

func (s *Store) RetryExtractionBatch(id string) error {
	return s.RetryExtractionBatchContext(context.Background(), id)
}

func (s *Store) RetryExtractionBatchContext(ctx context.Context, id string) error {
	if s.usesSeekDB() {
		return s.retryExtractionBatchSeekDB(ctx, id)
	}
	if !s.usesPostgres() {
		return ErrStoreBackendUnavailable
	}
	return s.retryExtractionBatchPostgres(ctx, id)
}
