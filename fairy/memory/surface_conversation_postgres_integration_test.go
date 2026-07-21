//go:build integration

package memory

import (
	"context"
	"strings"
	"sync"
	"testing"

	pgstore "fairy/postgres"
)

func TestSurfaceConversationIsolationAndLegacyDesktopIntegration(t *testing.T) {
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
	legacy, err := store.OpenOrCreateCharacterConversation("character-surface")
	if err != nil {
		t.Fatal(err)
	}
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	groupA, err := store.OpenOrCreateSurfaceConversation("character-surface", "im_group", digestA)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.OpenOrCreateSurfaceConversation("character-surface", "im_group", digestA)
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := store.OpenOrCreateSurfaceConversation("character-surface", "im_group", digestB)
	if err != nil {
		t.Fatal(err)
	}
	if groupA.Conversation.ID != again.Conversation.ID || groupA.Conversation.ID == groupB.Conversation.ID {
		t.Fatalf("group ids = %q/%q/%q", groupA.Conversation.ID, again.Conversation.ID, groupB.Conversation.ID)
	}
	reopenedLegacy, err := store.OpenOrCreateCharacterConversation("character-surface")
	if err != nil {
		t.Fatal(err)
	}
	if reopenedLegacy.Conversation.ID != legacy.Conversation.ID {
		t.Fatalf("legacy conversation = %q, want %q", reopenedLegacy.Conversation.ID, legacy.Conversation.ID)
	}
	var bindings, conversations int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM surface_conversations WHERE character_id = 'character-surface'").Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM conversations WHERE character_id = 'character-surface'").Scan(&conversations); err != nil {
		t.Fatal(err)
	}
	if bindings != 2 || conversations != 3 {
		t.Fatalf("bindings=%d conversations=%d", bindings, conversations)
	}
}

func TestSurfaceConversationConcurrentOpenCreatesOneBindingIntegration(t *testing.T) {
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
	digest := strings.Repeat("c", 64)
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bootstrap, err := store.OpenOrCreateSurfaceConversationContext(ctx, "character-concurrent", "desktop", digest)
			if err != nil {
				errs <- err
				return
			}
			ids <- bootstrap.Conversation.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var want string
	for id := range ids {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("conversation id = %q, want %q", id, want)
		}
	}
	var count int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM surface_conversations WHERE character_id = 'character-concurrent'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("binding count = %d, want 1", count)
	}
}
