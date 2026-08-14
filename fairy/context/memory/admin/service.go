package admin

import (
	"context"
	"fairy/context/memory/extraction"
	"fairy/context/memory/personal"
)

type Service struct {
	root       string
	store      *personal.Store
	extraction *extraction.Store
}

func NewService(root string) *Service {
	return &Service{root: root}
}

func NewServiceWithStore(root string, store *personal.Store, extractionStore *extraction.Store) *Service {
	return &Service{root: root, store: store, extraction: extractionStore}
}

func NewServiceFromStore(store *personal.Store) (*Service, error) {
	if store == nil {
		return nil, personal.ErrStoreBackendUnavailable
	}
	return &Service{store: store}, nil
}

func (s *Service) Summary() (personal.Summary, error) {
	return s.SummaryContext(context.Background())
}

func (s *Service) SummaryContext(ctx context.Context) (personal.Summary, error) {
	if s.store != nil {
		return s.store.SummaryContext(ctx)
	}
	return personal.Summary{}, personal.ErrStoreBackendUnavailable
}

func (s *Service) SemanticEmbeddingStatus() (personal.SemanticEmbeddingReadiness, error) {
	if s == nil || s.store == nil {
		return personal.SemanticEmbeddingReadiness{}, personal.ErrStoreBackendUnavailable
	}
	return s.store.SemanticEmbeddingStatus(context.Background())
}

func (s *Service) PersonalMemoryCatalog(characterID string) (personal.Catalog, error) {
	store, err := s.openStore()
	if err != nil {
		return personal.Catalog{}, err
	}
	return store.PersonalMemoryCatalog(characterID)
}

func (s *Service) CreatePersonalMemory(kind string, scope personal.Scope, content string, confidenceBasisPoints uint16) (personal.Record, error) {
	store, err := s.openStore()
	if err != nil {
		return personal.Record{}, err
	}
	return store.CreatePersonalMemory(kind, scope, content, confidenceBasisPoints)
}

func (s *Service) RevisePersonalMemory(id string, content string, confidenceBasisPoints uint16) (personal.Record, error) {
	store, err := s.openStore()
	if err != nil {
		return personal.Record{}, err
	}
	return store.RevisePersonalMemory(id, content, confidenceBasisPoints)
}

func (s *Service) TombstonePersonalMemory(id string) error {
	store, err := s.openStore()
	if err != nil {
		return err
	}
	return store.TombstonePersonalMemory(id)
}

func (s *Service) AssignLegacyRelationship(id string, characterID string) (personal.Record, error) {
	store, err := s.openStore()
	if err != nil {
		return personal.Record{}, err
	}
	return store.AssignLegacyRelationship(id, characterID)
}

func (s *Service) ExtractionBatchCatalog(characterID string) (extraction.Catalog, error) {
	if s == nil || s.extraction == nil {
		return extraction.Catalog{}, extraction.ErrStoreBackendUnavailable
	}
	return s.extraction.ExtractionBatchCatalog(characterID)
}

func (s *Service) RetryExtractionBatch(id string) error {
	if s == nil || s.extraction == nil {
		return extraction.ErrStoreBackendUnavailable
	}
	return s.extraction.RetryExtractionBatch(id)
}

func (s *Service) openStore() (*personal.Store, error) {
	if s.store != nil {
		return s.store, nil
	}
	return nil, personal.ErrStoreBackendUnavailable
}
