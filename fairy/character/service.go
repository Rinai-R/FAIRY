package character

import (
	"go.uber.org/zap"
)

type CharacterService struct {
	root   string
	store  *Store
	logger *zap.Logger
}

func NewCharacterService(root string) *CharacterService {
	return &CharacterService{
		root:   root,
		store:  NewStore(root),
		logger: zap.NewNop(),
	}
}

// CatalogStore returns the process-scoped character catalog store for sharing
// with other composition-root consumers (e.g. companion).
func (s *CharacterService) CatalogStore() *Store {
	if s == nil {
		return nil
	}
	return s.store
}

// AttachLogger injects the process logger (dependency injection, no global).
func AttachLogger(s *CharacterService, logger *zap.Logger) {
	if s == nil || logger == nil {
		return
	}
	s.logger = logger
}

func (s *CharacterService) ListCharacters() (Catalog, error) {
	catalog, err := s.store.List()
	if err != nil {
		s.logger.Error("list characters failed", zap.Error(err))
		return Catalog{}, err
	}
	activeName := ""
	if catalog.Active != nil {
		activeName = catalog.Active.Name
	}
	s.logger.Info("list characters", zap.Int("characters", len(catalog.Characters)), zap.String("active", activeName))
	return catalog, nil
}

func (s *CharacterService) CreateCharacter(brief Brief, visualPackID string) (Record, error) {
	record, err := s.store.Create(brief, visualPackID)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *CharacterService) UpdateCharacter(characterID string, brief Brief) (Record, error) {
	record, err := s.store.Update(characterID, brief)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *CharacterService) DeleteCharacter(characterID string) error {
	return s.store.Delete(characterID)
}

func (s *CharacterService) SetCharacterAppearance(characterID string, visualPackID string) (Record, error) {
	record, err := s.store.SetAppearance(characterID, visualPackID)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *CharacterService) ActivateCharacter(characterID string, revision uint64) (Record, error) {
	record, err := s.store.Activate(characterID, revision)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *CharacterService) ImportCharacterPackage(packagePath string) (Record, error) {
	record, err := s.store.ImportPackage(packagePath)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *CharacterService) ExportCharacterPackage(characterID string, outputPath string) error {
	return s.store.ExportPackage(characterID, outputPath)
}
