//go:build integration

package memory

import (
	"context"
	"testing"

	coredb "fairy/runtime/database"
)

func TestMessagePagePaginationIsCompleteAndStableIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := newMemoryIntegrationStores(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-page")
	if err != nil {
		t.Fatal(err)
	}
	conversationID := bootstrap.Conversation.ID
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, sequence, status, extraction_state, created_at_ms, updated_at_ms)
SELECT 'page-turn-' || g, $1, g, 'completed', 'ineligible', g, g
FROM generate_series(1, 205) AS g`, conversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms)
SELECT 'page-message-' || g, $1, 'page-turn-' || g, g, CASE WHEN g % 2 = 0 THEN 'assistant' ELSE 'user' END, 'message-' || g, g
FROM generate_series(1, 205) AS g`, conversationID); err != nil {
		t.Fatal(err)
	}

	seen := make(map[uint64]bool, 205)
	var before uint64
	var previousCursor uint64
	for {
		page, err := store.ListConversationMessagesBefore(conversationID, before, 50)
		if err != nil {
			t.Fatal(err)
		}
		for i, message := range page.Messages {
			if i > 0 && page.Messages[i-1].Sequence >= message.Sequence {
				t.Fatalf("page not ascending: %#v", page.Messages)
			}
			if seen[message.Sequence] {
				t.Fatalf("duplicate sequence %d", message.Sequence)
			}
			seen[message.Sequence] = true
		}
		if page.NextBeforeSequence == nil {
			break
		}
		if previousCursor != 0 && *page.NextBeforeSequence >= previousCursor {
			t.Fatalf("cursor did not decrease: %d then %d", previousCursor, *page.NextBeforeSequence)
		}
		previousCursor = *page.NextBeforeSequence
		before = *page.NextBeforeSequence
	}
	if len(seen) != 205 {
		t.Fatalf("seen %d messages, want 205", len(seen))
	}
	for sequence := uint64(1); sequence <= 205; sequence++ {
		if !seen[sequence] {
			t.Fatalf("missing sequence %d", sequence)
		}
	}
	if _, err := store.ListConversationMessagesBefore("missing-conversation", 0, 50); err == nil {
		t.Fatal("missing conversation returned an empty success page")
	}
}

func TestConversationMessageCorrelationSurvivesHistoryReloadIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := newMemoryIntegrationStores(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-message-correlation")
	if err != nil {
		t.Fatal(err)
	}
	const messageID = "debug-msg-browser-1"
	persisted, err := store.BeginCorrelatedTurnContext(ctx, bootstrap.Conversation.ID, "需要关联链路", messageID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.UserMessage.MessageID != messageID {
		t.Fatalf("persisted messageId = %q, want %q", persisted.UserMessage.MessageID, messageID)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, persisted.ID, "已经关联"); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListConversationMessagesBeforeContext(ctx, bootstrap.Conversation.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(page.Messages))
	}
	for _, message := range page.Messages {
		if message.MessageID != messageID {
			t.Fatalf("reloaded %s messageId = %q, want %q", message.Role, message.MessageID, messageID)
		}
	}

	reloaded, err := store.LoadConversationContext(ctx, bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 2 || reloaded.Messages[0].MessageID != messageID || reloaded.Messages[1].MessageID != messageID {
		t.Fatalf("reloaded correlation = %#v", reloaded.Messages)
	}
	if _, err := store.BeginCorrelatedTurnContext(ctx, bootstrap.Conversation.ID, "无效关联", "bad\nmessage"); err == nil {
		t.Fatal("invalid messageId was accepted")
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, message_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('turn-invalid-message-id', $1, E'bad\nmessage', 2, 'interpreting', 'user', 'ineligible', 1, 1)`, bootstrap.Conversation.ID); err == nil {
		t.Fatal("database constraint accepted a control character in message_id")
	}
}
