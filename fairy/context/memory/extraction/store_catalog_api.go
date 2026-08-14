package extraction

import "context"

func (s *Store) ExtractionBatchCatalog(characterID string) (Catalog, error) {
	return s.ExtractionBatchCatalogContext(context.Background(), characterID)
}

func (s *Store) ExtractionBatchCatalogContext(ctx context.Context, characterID string) (Catalog, error) {
	if !s.usesSeekDB() {
		return Catalog{}, ErrStoreBackendUnavailable
	}
	return s.extractionBatchCatalogSeekDB(ctx, characterID)
}

func (s *Store) RetryExtractionBatch(id string) error {
	return s.RetryExtractionBatchContext(context.Background(), id)
}

func (s *Store) RetryExtractionBatchContext(ctx context.Context, id string) error {
	if !s.usesSeekDB() {
		return ErrStoreBackendUnavailable
	}
	return s.retryExtractionBatchSeekDB(ctx, id)
}
