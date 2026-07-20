//go:build sqlite_legacy

package memory

import (
	"errors"
	"testing"
)

func TestMemoryServiceSummaryReturnsMissingDatabaseExplicitly(t *testing.T) {
	_, err := NewMemoryService(t.TempDir()).Summary()
	if !errors.Is(err, ErrDatabaseMissing) {
		t.Fatalf("Summary() error = %v, want %v", err, ErrDatabaseMissing)
	}
}

func TestMemoryServiceOpenOrCreateCharacterConversation(t *testing.T) {
	service := NewMemoryService(t.TempDir())
	bootstrap, err := service.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	if bootstrap.Conversation.CharacterID != "character-1" || bootstrap.PromptWindow.ConversationID != bootstrap.Conversation.ID {
		t.Fatalf("bootstrap = %#v", bootstrap)
	}
}

func TestMemoryServicePersonalMemoryLifecycle(t *testing.T) {
	service := NewMemoryService(t.TempDir())
	bootstrap, err := service.OpenOrCreateCharacterConversation("6a129284-6358-47b0-ad64-2a5907d36c91")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	store, err := service.openStore()
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "我在。"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	record, err := service.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "喜欢安静", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	catalog, err := service.PersonalMemoryCatalog(bootstrap.Conversation.CharacterID)
	if err != nil {
		t.Fatalf("PersonalMemoryCatalog() error = %v", err)
	}
	if len(catalog.Global) != 1 || catalog.Global[0].ID != record.ID {
		t.Fatalf("catalog = %#v", catalog)
	}
}
