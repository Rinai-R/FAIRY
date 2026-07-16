package memory

import (
	"path/filepath"
	"testing"
)

func seededMemoryStore(t *testing.T) (*Store, string) {
	t.Helper()
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("6a129284-6358-47b0-ad64-2a5907d36c91")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "我在。"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	return store, bootstrap.Conversation.CharacterID
}

func TestCreateAndListPersonalMemory(t *testing.T) {
	store, characterID := seededMemoryStore(t)
	global, err := store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "喜欢安静", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory(global) error = %v", err)
	}
	rel, err := store.CreatePersonalMemory("relationship", MemoryScope{Type: "character", CharacterID: characterID}, "信任角色", 8500)
	if err != nil {
		t.Fatalf("CreatePersonalMemory(character) error = %v", err)
	}
	catalog, err := store.PersonalMemoryCatalog(characterID)
	if err != nil {
		t.Fatalf("PersonalMemoryCatalog() error = %v", err)
	}
	if len(catalog.Global) != 1 || catalog.Global[0].ID != global.ID {
		t.Fatalf("global catalog = %#v", catalog.Global)
	}
	if len(catalog.Character) != 1 || catalog.Character[0].ID != rel.ID {
		t.Fatalf("character catalog = %#v", catalog.Character)
	}
}

func TestReviseAndTombstonePersonalMemory(t *testing.T) {
	store, characterID := seededMemoryStore(t)
	record, err := store.CreatePersonalMemory("relationship", MemoryScope{Type: "character", CharacterID: characterID}, "旧关系", 7000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	revised, err := store.RevisePersonalMemory(record.ID, "新关系", 9200)
	if err != nil {
		t.Fatalf("RevisePersonalMemory() error = %v", err)
	}
	if revised.SupersedesID == nil || *revised.SupersedesID != record.ID || revised.Content != "新关系" {
		t.Fatalf("revised = %#v", revised)
	}
	if err := store.TombstonePersonalMemory(revised.ID); err != nil {
		t.Fatalf("TombstonePersonalMemory() error = %v", err)
	}
	catalog, err := store.PersonalMemoryCatalog(characterID)
	if err != nil {
		t.Fatalf("PersonalMemoryCatalog() error = %v", err)
	}
	if len(catalog.Character) != 0 {
		t.Fatalf("character catalog = %#v", catalog.Character)
	}
}

func TestAssignLegacyRelationship(t *testing.T) {
	store, characterID := seededMemoryStore(t)
	legacy, err := store.CreatePersonalMemory("relationship", MemoryScope{Type: "unassigned_legacy"}, "旧关系", 6000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory(legacy) error = %v", err)
	}
	assigned, err := store.AssignLegacyRelationship(legacy.ID, characterID)
	if err != nil {
		t.Fatalf("AssignLegacyRelationship() error = %v", err)
	}
	if assigned.Scope.Type != "character" || assigned.Scope.CharacterID != characterID || assigned.SupersedesID == nil || *assigned.SupersedesID != legacy.ID {
		t.Fatalf("assigned = %#v", assigned)
	}
}

func TestCreatePersonalMemoryRequiresConversationSource(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	_, err = store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "喜欢安静", 9000)
	if err == nil {
		t.Fatal("CreatePersonalMemory() error = nil, want source conversation error")
	}
}
