//go:build integration

package memory

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"

	"fairy/identity"
	"fairy/interaction"
	pgstore "fairy/postgres"
	"fairy/secret"
)

func TestEndpointConversationIsolationAndImmutableFactsIntegration(t *testing.T) {
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

	legacy, err := store.OpenOrCreateCharacterConversation("character-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	binding := multiIMBinding()
	conversationA, err := store.OpenOrCreateEndpointConversation("character-endpoint", binding, digestA)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.OpenOrCreateEndpointConversation("character-endpoint", binding, digestA)
	if err != nil {
		t.Fatal(err)
	}
	conversationB, err := store.OpenOrCreateEndpointConversation("character-endpoint", binding, digestB)
	if err != nil {
		t.Fatal(err)
	}
	if conversationA.Conversation.ID != again.Conversation.ID || conversationA.Conversation.ID == conversationB.Conversation.ID {
		t.Fatalf("endpoint ids = %q/%q/%q", conversationA.Conversation.ID, again.Conversation.ID, conversationB.Conversation.ID)
	}

	mismatch := binding
	mismatch.Facts.Presentation = interaction.PresentationEmbodied
	if _, err := store.OpenOrCreateEndpointConversation("character-endpoint", mismatch, digestA); !errors.Is(err, ErrEndpointBindingMismatch) {
		t.Fatalf("mismatched reopen error = %v", err)
	}
	stored, ok, err := store.LookupEndpointForConversation(conversationA.Conversation.ID)
	if err != nil || !ok || stored != binding {
		t.Fatalf("stored binding = %#v, ok=%v, err=%v", stored, ok, err)
	}
	reopenedLegacy, err := store.OpenOrCreateCharacterConversation("character-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if reopenedLegacy.Conversation.ID != legacy.Conversation.ID {
		t.Fatalf("character conversation = %q, want %q", reopenedLegacy.Conversation.ID, legacy.Conversation.ID)
	}
}

func TestEndpointConversationConcurrentOpenCreatesOneBindingIntegration(t *testing.T) {
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
			bootstrap, err := store.OpenOrCreateEndpointConversationContext(ctx, "character-concurrent", desktopBinding(), digest)
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
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM endpoint_conversations WHERE character_id = 'character-concurrent'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("binding count = %d, want 1", count)
	}
}

func TestOwnerIdentityLifecycleDoesNotPersistRawSubjectIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	identityStore, err := identity.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.CipherFromEnv(func(name string) string {
		if name != secret.EnvMasterKey {
			return ""
		}
		return base64.StdEncoding.EncodeToString(bytesOf(7, 32))
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := interaction.PrincipalRef{Namespace: "qq.onebot", Subject: "raw-user-123456"}
	digest, err := cipher.DigestPrincipal(principal)
	if err != nil {
		t.Fatal(err)
	}
	if err := identityStore.BindOwner(principal.Namespace, digest); err != nil {
		t.Fatal(err)
	}
	if err := identityStore.BindOwner(principal.Namespace, digest); err != nil {
		t.Fatalf("idempotent bind: %v", err)
	}
	owner, err := identityStore.IsOwner(principal.Namespace, digest)
	if err != nil || !owner {
		t.Fatalf("IsOwner() = %v, %v", owner, err)
	}
	owners, err := identityStore.ListOwners()
	if err != nil || len(owners) != 1 || owners[0].Namespace != principal.Namespace || owners[0].PrincipalDigest != digest {
		t.Fatalf("ListOwners() = %#v, %v", owners, err)
	}

	var rawCount int
	if err := pool.Raw().QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM owner_identities WHERE namespace = $1 OR subject_digest = $1) +
    (SELECT count(*) FROM endpoint_conversations WHERE principal_namespace = $1 OR principal_digest = $1)`,
		principal.Subject,
	).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 0 {
		t.Fatalf("raw principal subject appears in %d durable rows", rawCount)
	}

	if err := identityStore.UnbindOwner(principal.Namespace, digest); err != nil {
		t.Fatal(err)
	}
	if err := identityStore.UnbindOwner(principal.Namespace, digest); !errors.Is(err, identity.ErrOwnerIdentityNotFound) {
		t.Fatalf("second UnbindOwner() error = %v", err)
	}
	owner, err = identityStore.IsOwner(principal.Namespace, digest)
	if err != nil || owner {
		t.Fatalf("IsOwner() after unbind = %v, %v", owner, err)
	}
}

func multiIMBinding() interaction.Binding {
	return interaction.Binding{
		Endpoint: interaction.EndpointIM,
		Facts: interaction.Facts{
			Audience: interaction.AudienceMulti, Initiation: interaction.InitiationAmbient,
			Presentation: interaction.PresentationChat,
		},
	}
}

func bytesOf(value byte, length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}
