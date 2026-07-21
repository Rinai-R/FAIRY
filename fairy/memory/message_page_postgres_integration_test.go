//go:build integration

package memory

import (
	"context"
	"testing"

	pgstore "fairy/postgres"
)

func TestMessagePagePaginationIsCompleteAndStableIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
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
