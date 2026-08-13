//go:build integration

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"fairy/runtime/seekdb"
)

func TestRealSeekDBConfigDocumentCASProfileAndRestart(t *testing.T) {
	instance, database, runtimeConfig := openSecretStoreSeekDBRuntime(t)
	closed := false
	defer func() {
		if !closed {
			closeSecretStoreSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
		}
	}()
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB foundation schema: %v", err)
	}

	store, err := NewSeekDBDocumentStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(t.Context(), "runtime", "agent"); err != nil || found {
		t.Fatalf("Get(absent) = (found %v, error %v)", found, err)
	}
	created, err := store.CompareAndSwap(t.Context(), "runtime", "agent", 1, 0, json.RawMessage(`{"mode":"desktop"}`))
	if err != nil || created.Revision != 1 || created.CreatedAtUnixMS != created.UpdatedAtUnixMS {
		t.Fatalf("CompareAndSwap(create) = (%#v, %v)", created, err)
	}
	if _, err := store.CompareAndSwap(t.Context(), "runtime", "agent", 1, 0, json.RawMessage(`{"mode":"stale"}`)); !errors.Is(err, ErrConfigRevisionConflict) {
		t.Fatalf("CompareAndSwap(stale create) error = %v", err)
	}
	assertOneConcurrentConfigCreate(t, store)

	const contenders = 8
	results := make(chan error, contenders)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.CompareAndSwap(
				t.Context(),
				"runtime",
				"agent",
				1,
				1,
				json.RawMessage(fmt.Sprintf(`{"winner":%d}`, index)),
			)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConfigRevisionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent CompareAndSwap error = %v", err)
		}
	}
	if successes != 1 || conflicts != contenders-1 {
		t.Fatalf("concurrent results = %d successes, %d conflicts", successes, conflicts)
	}
	current, found, err := store.Get(t.Context(), "runtime", "agent")
	if err != nil || !found || current.Revision != 2 {
		t.Fatalf("Get(after CAS) = (%#v, %v, %v)", current, found, err)
	}
	store.now = func() time.Time { return time.UnixMilli(current.CreatedAtUnixMS - 10_000) }
	replaced, err := store.CompareAndSwap(t.Context(), "runtime", "agent", 2, 2, json.RawMessage(`{"mode":"rollback-safe"}`))
	if err != nil {
		t.Fatalf("CompareAndSwap(clock rollback): %v", err)
	}
	if replaced.Revision != 3 || replaced.UpdatedAtUnixMS < current.UpdatedAtUnixMS {
		t.Fatalf("clock rollback result = %#v, previous = %#v", replaced, current)
	}
	store.now = time.Now
	if _, err := store.CompareAndSwap(t.Context(), "runtime", "agent", 2, 3, json.RawMessage(`[]`)); !errors.Is(err, ErrConfigDocumentInvalid) {
		t.Fatalf("CompareAndSwap(non-object) error = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.CompareAndSwap(canceled, "runtime", "agent", 2, 3, json.RawMessage(`{"mode":"canceled"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("CompareAndSwap(canceled) error = %v", err)
	}
	afterFailure, _, err := store.Get(t.Context(), "runtime", "agent")
	var afterFailureValue struct {
		Mode string `json:"mode"`
	}
	decodeErr := json.Unmarshal(afterFailure.Document, &afterFailureValue)
	if err != nil || decodeErr != nil || afterFailure.Revision != 3 || afterFailureValue.Mode != "rollback-safe" {
		t.Fatalf("document changed after rejected mutations: (%#v, %v)", afterFailure, err)
	}

	profiles, err := NewSeekDBProfileStore(store)
	if err != nil {
		t.Fatal(err)
	}
	preferredName := "  Rinai  "
	profileUpdate, err := profiles.SetPreferredName(&preferredName)
	if err != nil || !profileUpdate.Changed || profileUpdate.Profile == nil || profileUpdate.Profile.Revision != 1 || *profileUpdate.Profile.PreferredName != "Rinai" {
		t.Fatalf("SetPreferredName() = (%#v, %v)", profileUpdate, err)
	}
	same, err := profiles.SetPreferredName(&preferredName)
	if err != nil || same.Changed || same.Profile == nil || same.Profile.Revision != 1 {
		t.Fatalf("SetPreferredName(same) = (%#v, %v)", same, err)
	}

	closeSecretStoreSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB config document runtime: %v", err)
	}
	instance = restarted
	database = restarted.SQL()
	closed = false
	restartedDocuments, err := NewSeekDBDocumentStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	restartedProfiles, err := NewSeekDBProfileStore(restartedDocuments)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := restartedProfiles.Current()
	if err != nil || profile == nil || profile.Revision != 1 || *profile.PreferredName != "Rinai" {
		t.Fatalf("Current(after restart) = (%#v, %v)", profile, err)
	}
}

func assertOneConcurrentConfigCreate(t *testing.T, store *DocumentStore) {
	t.Helper()
	const contenders = 6
	results := make(chan error, contenders)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.CompareAndSwap(
				t.Context(),
				"runtime",
				"concurrent-create",
				1,
				0,
				json.RawMessage(fmt.Sprintf(`{"writer":%d}`, index)),
			)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConfigRevisionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent create error = %v", err)
		}
	}
	if successes != 1 || conflicts != contenders-1 {
		t.Fatalf("concurrent create = %d successes, %d conflicts", successes, conflicts)
	}
	document, found, err := store.Get(t.Context(), "runtime", "concurrent-create")
	if err != nil || !found || document.Revision != 1 {
		t.Fatalf("concurrent create result = (%#v, found %v, error %v)", document, found, err)
	}
}
