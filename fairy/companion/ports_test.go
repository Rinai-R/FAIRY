package companion

import (
	"errors"
	"strings"
	"testing"

	"fairy/character"
)

type stubCharacterLookup struct {
	record character.Record
	found  bool
	err    error
}

func (s stubCharacterLookup) Lookup(string) (character.Record, bool, error) {
	return s.record, s.found, s.err
}

func TestRespondRuntimeMigratedRequiresPorts(t *testing.T) {
	if NewCompanionService().RespondRuntimeMigrated() {
		t.Fatal("empty companion must not report migrated")
	}
	root := t.TempDir()
	service := NewCompanionServiceWithRuntime(root, nil, nil, nil)
	if service.RespondRuntimeMigrated() {
		t.Fatal("nil memory/model must not report migrated")
	}
}

func TestActiveCharacterUsesLookupResult(t *testing.T) {
	want := character.Record{CharacterID: "character-1", Revision: 3, Name: "Fairy"}
	service := &CompanionService{characterLookup: stubCharacterLookup{record: want, found: true}}
	got, err := service.activeCharacter(want.CharacterID)
	if err != nil {
		t.Fatalf("activeCharacter() error = %v", err)
	}
	if got.CharacterID != want.CharacterID || got.Revision != want.Revision || got.Name != want.Name {
		t.Fatalf("activeCharacter() = %#v, want %#v", got, want)
	}
}

func TestActiveCharacterReportsLookupFailures(t *testing.T) {
	if _, err := (*CompanionService)(nil).activeCharacter("character-1"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("nil service error = %v, want not configured", err)
	}
	service := &CompanionService{characterLookup: stubCharacterLookup{}}
	if _, err := service.activeCharacter("character-1"); err == nil || err.Error() != "character is not available" {
		t.Fatalf("not found error = %v, want character is not available", err)
	}
	want := errors.New("reading target character")
	service.characterLookup = stubCharacterLookup{err: want}
	if _, err := service.activeCharacter("character-1"); !errors.Is(err, want) {
		t.Fatalf("lookup error = %v, want %v", err, want)
	}
}
