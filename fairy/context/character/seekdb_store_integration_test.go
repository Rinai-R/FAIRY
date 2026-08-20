//go:build integration

package character

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fairy/runtime/seekdb"
)

func TestRealSeekDBCharacterStoreIsAtomicAndPersistsAcrossRestart(t *testing.T) {
	instance, database, runtimeConfig := openCharacterSeekDBRuntime(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeCharacterSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB foundation schema: %v", err)
	}

	root := t.TempDir()
	writeVisual(t, root, "fairy.atri")
	writeVisual(t, root, "fairy.alt")
	// A legacy file record must never become a fallback for the SeekDB store.
	writeCharacter(t, root, "legacy-only", 1, "旧文件角色")
	store, err := NewSeekDBStore(database, root, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if catalog, err := store.ListContext(t.Context()); err != nil || len(catalog.Characters) != 0 {
		t.Fatalf("empty authoritative catalog = (%#v, %v)", catalog, err)
	}

	created, err := store.CreateContext(t.Context(), Brief{
		Name: " 亚托莉 ", Description: " 认真听用户说话。 ",
		TextLanguage: "zh", SpeakingLanguage: "ja",
	}, "fairy.atri")
	if err != nil {
		t.Fatalf("CreateContext(): %v", err)
	}
	if created.Revision != 1 || created.Name != "亚托莉" || created.Appearance.BindingRevision != 1 {
		t.Fatalf("created character = %#v", created)
	}
	assertCharacterRowContract(t, database, created.CharacterID, 1, "fairy.atri", 1)

	active, err := store.ActivateContext(t.Context(), created.CharacterID, created.Revision)
	if err != nil || active.CharacterID != created.CharacterID {
		t.Fatalf("ActivateContext() = (%#v, %v)", active, err)
	}
	catalog, err := store.ListContext(t.Context())
	if err != nil || catalog.Active == nil || catalog.Active.CharacterID != created.CharacterID {
		t.Fatalf("active catalog = (%#v, %v)", catalog, err)
	}

	updated, err := store.UpdateContext(t.Context(), created.CharacterID, Brief{
		Name: "亚托莉", Description: "会先听完再回应。", TextLanguage: "en", SpeakingLanguage: "zh",
	})
	if err != nil {
		t.Fatalf("UpdateContext(): %v", err)
	}
	if updated.Revision != 2 || updated.Description != "会先听完再回应。" {
		t.Fatalf("updated character = %#v", updated)
	}
	assigned, err := store.SetAppearanceContext(t.Context(), created.CharacterID, "fairy.alt")
	if err != nil {
		t.Fatalf("SetAppearanceContext(): %v", err)
	}
	if assigned.Revision != 2 || assigned.Appearance.BindingRevision != 2 || assigned.Appearance.Visual == nil || assigned.Appearance.Visual.PackID != "fairy.alt" {
		t.Fatalf("assigned character = %#v", assigned)
	}

	const concurrentUpdates = 6
	var wait sync.WaitGroup
	errorsOut := make(chan error, concurrentUpdates)
	for index := range concurrentUpdates {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, updateErr := store.UpdateContext(t.Context(), created.CharacterID, Brief{
				Name: "亚托莉", Description: fmt.Sprintf("并发更新 %d", index), TextLanguage: "zh", SpeakingLanguage: "ja",
			})
			errorsOut <- updateErr
		}()
	}
	wait.Wait()
	close(errorsOut)
	for updateErr := range errorsOut {
		if updateErr != nil {
			t.Fatalf("concurrent UpdateContext(): %v", updateErr)
		}
	}
	afterConcurrent, found, err := store.LookupContext(t.Context(), created.CharacterID)
	if err != nil || !found || afterConcurrent.Revision != 2+concurrentUpdates {
		t.Fatalf("LookupContext(after concurrent updates) = (%#v, %v, %v)", afterConcurrent, found, err)
	}

	injected := errors.New("injected pre-commit failure")
	store.seekDB.mutationHook = func(operation string) error {
		if operation == "update" || operation == "delete" {
			return injected
		}
		return nil
	}
	if _, err := store.UpdateContext(t.Context(), created.CharacterID, Brief{
		Name: "不应提交", Description: "失败更新", TextLanguage: "zh", SpeakingLanguage: "ja",
	}); !errors.Is(err, injected) {
		t.Fatalf("injected UpdateContext() error = %v", err)
	}
	afterFailedUpdate, found, err := store.LookupContext(t.Context(), created.CharacterID)
	if err != nil || !found || afterFailedUpdate.Revision != afterConcurrent.Revision || afterFailedUpdate.Name != afterConcurrent.Name {
		t.Fatalf("failed update changed character = (%#v, %v, %v)", afterFailedUpdate, found, err)
	}
	if err := store.DeleteContext(t.Context(), created.CharacterID); !errors.Is(err, injected) {
		t.Fatalf("injected DeleteContext() error = %v", err)
	}
	afterFailedDelete, found, err := store.LookupContext(t.Context(), created.CharacterID)
	if err != nil || !found || afterFailedDelete.Revision != afterConcurrent.Revision {
		t.Fatalf("failed delete changed character = (%#v, %v, %v)", afterFailedDelete, found, err)
	}
	if catalog, err := store.ListContext(t.Context()); err != nil || catalog.Active == nil || catalog.Active.CharacterID != created.CharacterID {
		t.Fatalf("failed delete changed active selection = (%#v, %v)", catalog, err)
	}
	store.seekDB.mutationHook = nil

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.ListContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListContext(canceled) error = %v, want context.Canceled", err)
	}

	closeCharacterSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB character runtime: %v", err)
	}
	instance, database, closed = restarted, restarted.SQL(), false
	restartedStore, err := NewSeekDBStore(database, root, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	restored, found, err := restartedStore.LookupContext(t.Context(), created.CharacterID)
	if err != nil || !found || restored.Revision != afterConcurrent.Revision || restored.Appearance.Visual == nil || restored.Appearance.Visual.PackID != "fairy.alt" {
		t.Fatalf("restarted LookupContext() = (%#v, %v, %v)", restored, found, err)
	}
	if catalog, err := restartedStore.ListContext(t.Context()); err != nil || catalog.Active == nil || catalog.Active.CharacterID != created.CharacterID {
		t.Fatalf("restarted catalog = (%#v, %v)", catalog, err)
	}
	if err := restartedStore.DeleteContext(t.Context(), created.CharacterID); err != nil {
		t.Fatalf("DeleteContext(): %v", err)
	}
	if _, found, err := restartedStore.LookupContext(t.Context(), created.CharacterID); err != nil || found {
		t.Fatalf("LookupContext(after delete) = (found %v, error %v)", found, err)
	}
	if catalog, err := restartedStore.ListContext(t.Context()); err != nil || catalog.Active != nil || len(catalog.Characters) != 0 {
		t.Fatalf("catalog after delete = (%#v, %v)", catalog, err)
	}
}

func openCharacterSeekDBRuntime(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath: library,
		DataDir:     filepath.Join(t.TempDir(), "seekdb-data"),
		Database:    seekdb.DefaultDatabase,
		ConnectLimit: 5 * time.Second, StartLimit: 90 * time.Second, QueryLimit: 15 * time.Second,
		ShutdownLimit: 20 * time.Second, MaxOpenConns: 8, MaxIdleConns: 4,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveCharacterLoopbackAddress(t *testing.T) string {
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

func closeCharacterSeekDBRuntime(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB runtime: %v", err)
	}
}

func assertCharacterRowContract(t *testing.T, database *sql.DB, characterID string, wantRevision uint64, wantPackID string, wantBindingRevision uint64) {
	t.Helper()
	var revision uint64
	var name string
	var snapshotJSON, appearanceJSON []byte
	if err := database.QueryRowContext(t.Context(), `
SELECT revision, name, snapshot, appearance_ref
FROM characters
WHERE character_id = ?`, characterID).Scan(&revision, &name, &snapshotJSON, &appearanceJSON); err != nil {
		t.Fatal(err)
	}
	var snapshot characterSnapshot
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		t.Fatalf("decode stored snapshot: %v", err)
	}
	var appearance seekDBAppearanceRef
	if err := json.Unmarshal(appearanceJSON, &appearance); err != nil {
		t.Fatalf("decode stored appearance ref: %v", err)
	}
	if revision != wantRevision || snapshot.Revision != wantRevision || snapshot.CharacterID != characterID || snapshot.Identity.Name != name {
		t.Fatalf("stored snapshot = revision %d, snapshot %#v, name %q", revision, snapshot, name)
	}
	if appearance.VisualPackID != wantPackID || appearance.BindingRevision != wantBindingRevision {
		t.Fatalf("stored appearance ref = %#v", appearance)
	}
}
