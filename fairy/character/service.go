package character

import (
	"log"

	"fairy/notify"
)

type CharacterService struct {
	root string
}

func NewCharacterService(root string) *CharacterService {
	return &CharacterService{root: root}
}

func (s *CharacterService) ListCharacters() (Catalog, error) {
	catalog, err := NewStore(s.root).List()
	if err != nil {
		log.Printf("ListCharacters error: %v", err)
		return Catalog{}, err
	}
	activeName := ""
	if catalog.Active != nil {
		activeName = catalog.Active.Name
	}
	log.Printf("ListCharacters ok characters=%d active=%q", len(catalog.Characters), activeName)
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
