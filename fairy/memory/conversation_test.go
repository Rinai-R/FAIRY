//go:build sqlite_legacy

package memory

import (
	"path/filepath"
	"testing"
)

func TestOpenOrCreateCharacterConversationCreatesAndReloads(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	if bootstrap.Conversation.ID == "" || bootstrap.Conversation.CharacterID != "character-1" {
		t.Fatalf("conversation = %#v", bootstrap.Conversation)
	}
	if bootstrap.PromptWindow.ConversationID != bootstrap.Conversation.ID || bootstrap.PromptWindow.Revision != 1 {
		t.Fatalf("prompt window = %#v", bootstrap.PromptWindow)
	}

	reloaded, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("reload conversation error = %v", err)
	}
	if reloaded.Conversation.ID != bootstrap.Conversation.ID {
		t.Fatalf("reloaded conversation id = %q, want %q", reloaded.Conversation.ID, bootstrap.Conversation.ID)
	}
}

func TestBeginAndCompleteTurnPersistMessagesInOrder(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if turn.UserMessage.Sequence != 1 || turn.UserMessage.Role != "user" || turn.UserMessage.Content != "你好" {
		t.Fatalf("turn = %#v", turn)
	}
	assistant, err := store.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "我在。")
	if err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	if assistant.Sequence != 2 || assistant.Role != "assistant" || assistant.Content != "我在。" {
		t.Fatalf("assistant message = %#v", assistant)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(reloaded.Messages) != 2 {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
	if reloaded.Messages[0].Role != "user" || reloaded.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
}

func TestFailTurnDoesNotWriteAssistantMessage(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if err := store.FailTurn(bootstrap.Conversation.ID, turn.ID, "MODEL_FAILED", "模型失败", false); err != nil {
		t.Fatalf("FailTurn() error = %v", err)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "不该写入"); err == nil {
		t.Fatal("CompleteTurn() after FailTurn error = nil, want terminal turn error")
	}
}

func TestBeginTurnRejectsMissingConversationWithoutWriting(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	if _, err := store.BeginTurn("missing-conversation", "你好"); err == nil {
		t.Fatal("BeginTurn() error = nil, want missing conversation error")
	}
	summary, err := store.Summary()
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if summary.Conversations != 0 {
		t.Fatalf("Conversations = %d, want 0", summary.Conversations)
	}
}

func TestConversationValidationRejectsInvalidContent(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	if _, err := store.BeginTurn(bootstrap.Conversation.ID, " "); err == nil {
		t.Fatal("BeginTurn() blank message error = nil, want error")
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, turn.ID, ""); err == nil {
		t.Fatal("CompleteTurn() blank assistant error = nil, want error")
	}
}
