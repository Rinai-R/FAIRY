package memory

import (
	"path/filepath"
	"testing"
)

func TestCommitPromptWindowAdvancesRevisionAndCutoff(t *testing.T) {
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
	result, err := store.CommitPromptWindow(bootstrap.Conversation.ID, bootstrap.PromptWindow.Revision, " 用户正在问候，角色回应在场。 ")
	if err != nil {
		t.Fatalf("CommitPromptWindow() error = %v", err)
	}
	if result.WindowRevision != 2 {
		t.Fatalf("WindowRevision = %d", result.WindowRevision)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if reloaded.PromptWindow.Summary == nil || *reloaded.PromptWindow.Summary != "用户正在问候，角色回应在场。" || reloaded.PromptWindow.CutoffMessageSequence != 2 {
		t.Fatalf("prompt window = %#v", reloaded.PromptWindow)
	}
}

func TestCommitPromptWindowRejectsStaleRevision(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("6a129284-6358-47b0-ad64-2a5907d36c91")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	if _, err := store.CommitPromptWindow(bootstrap.Conversation.ID, 99, "summary"); err == nil {
		t.Fatal("CommitPromptWindow() error = nil, want stale revision error")
	}
}
