package memory

import "context"

type MemoryService struct {
	root  string
	store *Store
}

func NewMemoryService(root string) *MemoryService {
	return &MemoryService{root: root}
}

func NewMemoryServiceWithStore(root string, store *Store) *MemoryService {
	return &MemoryService{root: root, store: store}
}

func NewMemoryServiceFromStore(store *Store) (*MemoryService, error) {
	if store == nil {
		return nil, ErrDatabasePoolEmpty
	}
	return &MemoryService{store: store}, nil
}

func (s *MemoryService) Summary() (Summary, error) {
	return s.SummaryContext(context.Background())
}

func (s *MemoryService) SummaryContext(ctx context.Context) (Summary, error) {
	if s.store != nil {
		return s.store.SummaryContext(ctx)
	}
	return Summary{}, ErrDatabasePoolEmpty
}

func (s *MemoryService) SemanticEmbeddingStatus() (SemanticEmbeddingReadiness, error) {
	if s == nil || s.store == nil {
		return SemanticEmbeddingReadiness{}, ErrDatabasePoolEmpty
	}
	queryCtx, cancel := s.store.pool.QueryContext(context.Background())
	defer cancel()
	vectorRows, err := countPostgresScalar(queryCtx, s.store.pool, `
SELECT
  (SELECT count(*) FROM personal_memories WHERE embedding_v2 IS NOT NULL)
  + (SELECT count(*) FROM knowledge_entries WHERE embedding_v2 IS NOT NULL)`)
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	status := SemanticStatusUnavailable
	reason := "api_embedder_required"
	if s.store.semanticEmbedder.Ready() {
		status = SemanticStatusReady
		reason = ""
	}
	return SemanticEmbeddingReadiness{
		Dimensions:     SemanticEmbeddingDimensions,
		DatabaseStatus: SemanticDatabaseStatusReady,
		SemanticStatus: string(status),
		Reason:         reason,
		VectorRows:     vectorRows,
	}, nil
}

func (s *MemoryService) OpenOrCreateCharacterConversation(characterID string) (ConversationBootstrap, error) {
	store, err := s.openStore()
	if err != nil {
		return ConversationBootstrap{}, err
	}
	return store.OpenOrCreateCharacterConversation(characterID)
}

func (s *MemoryService) PersonalMemoryCatalog(characterID string) (PersonalMemoryCatalog, error) {
	store, err := s.openStore()
	if err != nil {
		return PersonalMemoryCatalog{}, err
	}
	return store.PersonalMemoryCatalog(characterID)
}

func (s *MemoryService) CreatePersonalMemory(kind string, scope MemoryScope, content string, confidenceBasisPoints uint16) (PersonalMemoryRecord, error) {
	store, err := s.openStore()
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	return store.CreatePersonalMemory(kind, scope, content, confidenceBasisPoints)
}

func (s *MemoryService) RevisePersonalMemory(id string, content string, confidenceBasisPoints uint16) (PersonalMemoryRecord, error) {
	store, err := s.openStore()
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	return store.RevisePersonalMemory(id, content, confidenceBasisPoints)
}

func (s *MemoryService) TombstonePersonalMemory(id string) error {
	store, err := s.openStore()
	if err != nil {
		return err
	}
	return store.TombstonePersonalMemory(id)
}

func (s *MemoryService) AssignLegacyRelationship(id string, characterID string) (PersonalMemoryRecord, error) {
	store, err := s.openStore()
	if err != nil {
		return PersonalMemoryRecord{}, err
	}
	return store.AssignLegacyRelationship(id, characterID)
}

func (s *MemoryService) KnowledgeCatalog() (KnowledgeCatalog, error) {
	store, err := s.openStore()
	if err != nil {
		return KnowledgeCatalog{}, err
	}
	return store.KnowledgeCatalog()
}

func (s *MemoryService) ConfirmKnowledgeCandidate(id string) (KnowledgeRecord, error) {
	store, err := s.openStore()
	if err != nil {
		return KnowledgeRecord{}, err
	}
	return store.ConfirmKnowledgeCandidate(id)
}

func (s *MemoryService) TombstoneKnowledge(id string) error {
	store, err := s.openStore()
	if err != nil {
		return err
	}
	return store.TombstoneKnowledge(id)
}

func (s *MemoryService) ExtractionBatchCatalog(characterID string) (ExtractionBatchCatalog, error) {
	store, err := s.openStore()
	if err != nil {
		return ExtractionBatchCatalog{}, err
	}
	return store.ExtractionBatchCatalog(characterID)
}

func (s *MemoryService) RetryExtractionBatch(id string) error {
	store, err := s.openStore()
	if err != nil {
		return err
	}
	return store.RetryExtractionBatch(id)
}

func (s *MemoryService) TokenUsageReport() (UsageReport, error) {
	store, err := s.openStore()
	if err != nil {
		return UsageReport{}, err
	}
	return store.AggregateTokenUsage(DefaultUsageTurnLimit)
}

func (s *MemoryService) openStore() (*Store, error) {
	if s.store != nil {
		return s.store, nil
	}
	return nil, ErrDatabasePoolEmpty
}
