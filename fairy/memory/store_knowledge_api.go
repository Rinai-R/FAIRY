package memory

import "context"

func (s *Store) KnowledgeCatalog() (KnowledgeCatalog, error) {
	return s.KnowledgeCatalogContext(context.Background())
}

func (s *Store) KnowledgeCatalogContext(ctx context.Context) (KnowledgeCatalog, error) {
	return s.knowledgeCatalogPostgres(ctx)
}

func (s *Store) ConfirmKnowledgeCandidate(id string) (KnowledgeRecord, error) {
	return s.ConfirmKnowledgeCandidateContext(context.Background(), id)
}

func (s *Store) ConfirmKnowledgeCandidateContext(ctx context.Context, id string) (KnowledgeRecord, error) {
	return s.confirmKnowledgeCandidatePostgres(ctx, id)
}

func (s *Store) TombstoneKnowledge(id string) error {
	return s.TombstoneKnowledgeContext(context.Background(), id)
}

func (s *Store) TombstoneKnowledgeContext(ctx context.Context, id string) error {
	return s.tombstoneKnowledgePostgres(ctx, id)
}

func (s *Store) ExtractionBatchCatalog(characterID string) (ExtractionBatchCatalog, error) {
	return s.ExtractionBatchCatalogContext(context.Background(), characterID)
}

func (s *Store) ExtractionBatchCatalogContext(ctx context.Context, characterID string) (ExtractionBatchCatalog, error) {
	return s.extractionBatchCatalogPostgres(ctx, characterID)
}

func (s *Store) RetryExtractionBatch(id string) error {
	return s.RetryExtractionBatchContext(context.Background(), id)
}

func (s *Store) RetryExtractionBatchContext(ctx context.Context, id string) error {
	return s.retryExtractionBatchPostgres(ctx, id)
}
