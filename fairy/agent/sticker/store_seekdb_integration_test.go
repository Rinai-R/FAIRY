//go:build integration

package sticker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fairy/runtime/seekdb"
)

func TestRealSeekDBStickerStorePersistsCatalogAndContentConsistency(t *testing.T) {
	instance, database, runtimeConfig := openStickerSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeStickerSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB sticker schema: %v", err)
	}

	contentRoot := filepath.Join(t.TempDir(), "sticker-content")
	if _, err := NewSeekDBStore(nil, contentRoot, runtimeConfig.QueryLimit); !errors.Is(err, ErrSeekDBRequired) {
		t.Fatalf("NewSeekDBStore(nil) error = %v", err)
	}
	if _, err := NewSeekDBStore(database, "relative", runtimeConfig.QueryLimit); !errors.Is(err, ErrContentRootInvalid) {
		t.Fatalf("NewSeekDBStore(relative root) error = %v", err)
	}
	if _, err := NewSeekDBStore(database, contentRoot, 0); !errors.Is(err, ErrQueryLimitInvalid) {
		t.Fatalf("NewSeekDBStore(zero limit) error = %v", err)
	}
	store, err := NewSeekDBStore(database, contentRoot, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if false || !store.usesSeekDB() {
		t.Fatal("SeekDB sticker store reported a PostgreSQL fallback")
	}

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
	assertStickerContentFile(t, contentRoot, png)
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

	corrupt := append([]byte(nil), png...)
	corrupt[len(corrupt)-1] = 'x'
	if err := os.WriteFile(filepath.Join(contentRoot, content.ContentSHA256), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Content(t.Context(), draft.ID); !errors.Is(err, ErrContentInconsistent) {
		t.Fatalf("corrupt Content() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(contentRoot, content.ContentSHA256), png, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(contentRoot, content.ContentSHA256)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Content(t.Context(), draft.ID); !errors.Is(err, ErrContentInconsistent) {
		t.Fatalf("missing Content() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(contentRoot, content.ContentSHA256), png, 0o600); err != nil {
		t.Fatal(err)
	}

	gif := append([]byte("GIF89a"), []byte("two")...)
	undescribable, err := store.Create(t.Context(), CreateInput{Content: gif, DeclaredMIMEType: "image/gif"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(t.Context(), undescribable.ID, UpdateInput{Status: &active}); !errors.Is(err, ErrDescriptionRequired) {
		t.Fatalf("Update(active) error = %v", err)
	}

	closeStickerSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB sticker runtime: %v", err)
	}
	instance = restarted
	closed = false
	restartedStore, err := NewSeekDBStore(restarted.SQL(), contentRoot, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	found, err := restartedStore.Find(t.Context(), draft.ID)
	if err != nil || found.Status != StatusActive || found.ContentSHA256 != draft.ContentSHA256 {
		t.Fatalf("restart Find() = %#v, %v", found, err)
	}
	restored, err := restartedStore.Content(t.Context(), draft.ID)
	if err != nil || string(restored.Bytes) != string(png) {
		t.Fatalf("restart Content() = %#v, %v", restored, err)
	}
	if err := restartedStore.Delete(t.Context(), draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := restartedStore.Find(t.Context(), draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(deleted) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(contentRoot, draft.ContentSHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted content file still exists: %v", err)
	}
}

func assertStickerContentFile(t *testing.T, root string, content []byte) {
	t.Helper()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("content directory mode = %o, want 0700", info.Mode().Perm())
	}
	sum := sha256.Sum256(content)
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Mode().Perm() != 0o600 {
		t.Fatalf("content file mode = %o, want 0600", file.Mode().Perm())
	}
	if file.Mode().Type() != fs.FileMode(0) {
		t.Fatalf("content path is not a regular file: %v", file.Mode())
	}
}

func openStickerSeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath:    library,
		DataDir:       filepath.Join(t.TempDir(), "seekdb-sticker"),
		Database:      seekdb.DefaultDatabase,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  16,
		MaxIdleConns:  8,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB sticker runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveStickerLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func closeStickerSeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB sticker runtime: %v", err)
	}
}
