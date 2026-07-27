//go:build integration

package sticker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"fairy/coredb"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreLifecycleIntegration(t *testing.T) {
	store, cleanup := openIntegrationStore(t)
	defer cleanup()

	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("one")...)
	draft, err := store.Create(t.Context(), CreateInput{
		Content: png, DeclaredMIMEType: "image/png",
		Description: "震惊和无语", Tags: []string{"震惊", "无语"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Status != StatusDraft {
		t.Fatalf("status = %q, want draft", draft.Status)
	}
	if _, err := store.Create(t.Context(), CreateInput{Content: png}); !errors.Is(err, ErrDuplicateContent) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	active := StatusActive
	updated, err := store.Update(t.Context(), draft.ID, UpdateInput{Status: &active})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusActive {
		t.Fatalf("updated status = %q", updated.Status)
	}
	hasActive, err := store.HasActive(t.Context())
	if err != nil || !hasActive {
		t.Fatalf("HasActive() = %v, %v", hasActive, err)
	}
	candidates, err := store.Search(t.Context(), "无语", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ID != draft.ID || candidates[0].MIMEType != "image/png" {
		t.Fatalf("candidates = %#v", candidates)
	}
	page, err := store.List(t.Context(), ListInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != draft.ID {
		t.Fatalf("page = %#v", page)
	}
	content, err := store.Content(t.Context(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(content.Bytes) != string(png) || content.MIMEType != "image/png" {
		t.Fatalf("content = %#v", content)
	}
	if err := store.Delete(t.Context(), draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Find(t.Context(), draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(deleted) error = %v", err)
	}
}

func TestStoreRejectsActiveWithoutDescriptionIntegration(t *testing.T) {
	store, cleanup := openIntegrationStore(t)
	defer cleanup()
	gif := append([]byte("GIF89a"), []byte("two")...)
	record, err := store.Create(t.Context(), CreateInput{Content: gif, DeclaredMIMEType: "image/gif"})
	if err != nil {
		t.Fatal(err)
	}
	active := StatusActive
	if _, err := store.Update(t.Context(), record.ID, UpdateInput{Status: &active}); !errors.Is(err, ErrDescriptionRequired) {
		t.Fatalf("Update(active) error = %v", err)
	}
}

func openIntegrationStore(t *testing.T) (*Store, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	rawURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if rawURL == "" {
		rawURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fairy_sticker_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	pool, err := coredb.Open(ctx, coredb.ShortTimeoutConfig(parsed.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	store, err := NewStore(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		cleanupPool, openErr := pgxpool.New(cleanupCtx, rawURL)
		if openErr != nil {
			return
		}
		defer cleanupPool.Close()
		_, _ = cleanupPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	}
	return store, cleanup
}
