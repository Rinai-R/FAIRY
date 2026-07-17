package character

import (
	"fairy/notify"

	"go.uber.org/zap"
)

type CharacterService struct {
	root   string
	logger *zap.Logger
}

func NewCharacterService(root string) *CharacterService {
	return &CharacterService{root: root, logger: zap.NewNop()}
}

// AttachLogger injects the process logger (dependency injection, no global).
func AttachLogger(s *CharacterService, logger *zap.Logger) {
	if s == nil || logger == nil {
		return
	}
	s.logger = logger
}

func (s *CharacterService) ListCharacters() (Catalog, error) {
	catalog, err := NewStore(s.root).List()
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
	record, err := NewStore(s.root).Create(brief, visualPackID)
	if err != nil {
		return Record{}, err
	}
	notify.Emit(notify.CharacterChanged(record.Revision))
	return record, nil
}

func (s *CharacterService) UpdateCharacter(characterID string, brief Brief) (Record, error) {
	record, err := NewStore(s.root).Update(characterID, brief)
	if err != nil {
		return Record{}, err
	}
	notify.Emit(notify.CharacterChanged(record.Revision))
	return record, nil
}

func (s *CharacterService) SetCharacterAppearance(characterID string, visualPackID string) (Record, error) {
	record, err := NewStore(s.root).SetAppearance(characterID, visualPackID)
	if err != nil {
		return Record{}, err
	}
	notify.Emit(notify.CharacterChanged(record.Revision))
	return record, nil
}

func (s *CharacterService) ActivateCharacter(characterID string, revision uint64) (Record, error) {
	record, err := NewStore(s.root).Activate(characterID, revision)
	if err != nil {
		return Record{}, err
	}
	notify.Emit(notify.CharacterChanged(record.Revision))
	return record, nil
}

func (s *CharacterService) ImportCharacterPackage(packagePath string) (Record, error) {
	record, err := NewStore(s.root).ImportPackage(packagePath)
	if err != nil {
		return Record{}, err
	}
	notify.Emit(notify.CharacterChanged(record.Revision))
	return record, nil
}

func (s *CharacterService) ExportCharacterPackage(characterID string, outputPath string) error {
	return NewStore(s.root).ExportPackage(characterID, outputPath)
}
