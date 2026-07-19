package memory

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

func (s *MemoryService) Summary() (Summary, error) {
	if s.store != nil {
		return s.store.Summary()
	}
	dbPath, err := DatabasePath(s.root)
	if err != nil {
		return Summary{}, err
	}
	// Summary stays read-only and must not create a missing database.
	return NewStore(dbPath).Summary()
}

func (s *MemoryService) SemanticEmbeddingStatus() (SemanticEmbeddingReadiness, error) {
	return LocalSemanticEmbeddingStatus(s.root)
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
	dbPath, err := DatabasePath(s.root)
	if err != nil {
		return nil, err
	}
	store, err := OpenOrCreate(dbPath)
	if err != nil {
		return nil, err
	}
	s.store = store
	return store, nil
}
