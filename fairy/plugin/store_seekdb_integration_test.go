//go:build integration

package plugin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fairy/runtime/seekdb"
)

func TestRealSeekDBPluginStorePersistsStateStatsJournalAndSecretRefs(t *testing.T) {
	instance, database, runtimeConfig := openPluginSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closePluginSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate plugin schema: %v", err)
	}
	store, err := NewStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}

	pkg := PackageRecord{
		ID:               "fairy.plugin.example",
		Version:          "1.0.0",
		ABIVersion:       1,
		ArtifactSHA256:   sha256.Sum256([]byte("module-v1")),
		VerifiedAtUnixMS: 1,
		Manifest: Manifest{
			SchemaVersion: 1, ID: "fairy.plugin.example", Version: "1.0.0",
			ABI: ABIRange{Min: 1, Max: 1}, Entry: EntryModule, Exports: RequiredExports(),
			Capabilities:        []string{"state.read", "state.write"},
			ConfigSchemaVersion: 1, DataSchemaVersion: 1,
		},
	}
	if err := store.PutPackage(t.Context(), pkg); err != nil {
		t.Fatal(err)
	}
	record := InstanceRecord{
		ID:               "echo-1",
		PluginID:         pkg.ID,
		PluginVersion:    pkg.Version,
		Enabled:          true,
		Lifecycle:        "ready",
		CapabilityGrants: []string{"state.read", "state.write"},
		ConfigDocument:   json.RawMessage(`{"endpoint":"https://example.invalid"}`),
	}
	if err := store.PutInstance(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.PutState(t.Context(), record.ID, "cursor", "42"); err != nil {
		t.Fatal(err)
	}
	value, found, err := store.State(t.Context(), record.ID, "cursor")
	if err != nil || !found || value != "42" {
		t.Fatalf("State() = (%q, %v, %v)", value, found, err)
	}
	_, found, err = store.State(t.Context(), "other-1", "cursor")
	if err != nil || found {
		t.Fatalf("cross-instance state = (%v, %v)", found, err)
	}
	if err := store.RecordStats(t.Context(), StatsRecord{InstanceID: record.ID, GuestCalls: 3, HostCalls: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUpgrade(t.Context(), UpgradeRecord{
		JournalID: "up-1", InstanceID: record.ID, FromVersion: "1.0.0", ToVersion: "1.0.1",
		Status: "started", StartedAtUnixMS: 10,
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.Instances(t.Context())
	if err != nil || len(listed) != 1 || listed[0].ID != record.ID || listed[0].PluginVersion != "1.0.0" {
		t.Fatalf("Instances() = (%#v, %v)", listed, err)
	}
	upgrades, err := store.Upgrades(t.Context(), record.ID)
	if err != nil || len(upgrades) != 1 || upgrades[0].JournalID != "up-1" || upgrades[0].ErrorMessage != "" {
		t.Fatalf("Upgrades() = (%#v, %v)", upgrades, err)
	}

	if _, err := database.ExecContext(t.Context(), `
INSERT INTO secret_values(namespace, name, key_version, nonce, ciphertext, aad, created_at_ms, updated_at_ms)
VALUES ('plugin', 'search', 1, ?, ?, 'plugin.search', 1, 1)`,
		[]byte("0123456789ab"), []byte("cipher")); err != nil {
		t.Fatalf("insert secret ref target: %v", err)
	}
	if err := store.PutConfigRef(t.Context(), ConfigRef{
		InstanceID: record.ID, Handle: "search", SecretNamespace: "plugin", SecretName: "search",
	}); err != nil {
		t.Fatal(err)
	}

	var leaked int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM plugin_instance_config_refs WHERE handle = 'search' AND (
  CAST(secret_namespace AS CHAR) LIKE '%sk-live%' OR CAST(secret_name AS CHAR) LIKE '%sk-live%')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("config refs stored credential plaintext")
	}

	closePluginSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, database, runtimeConfig := reopenPluginSeekDB(t, runtimeConfig)
	t.Cleanup(func() { closePluginSeekDB(t, restarted, runtimeConfig.ShutdownLimit) })
	store, err = NewStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	value, found, err = store.State(t.Context(), record.ID, "cursor")
	if err != nil || !found || value != "42" {
		t.Fatalf("State() after restart = (%q, %v, %v)", value, found, err)
	}
}

func TestRealSeekDBPluginStoreRejectsSecretfulConfig(t *testing.T) {
	instance, database, runtimeConfig := openPluginSeekDB(t)
	t.Cleanup(func() { closePluginSeekDB(t, instance, runtimeConfig.ShutdownLimit) })
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	err = store.PutInstance(t.Context(), InstanceRecord{
		ID: "echo-2", PluginID: "fairy.plugin.example", PluginVersion: "1.0.0",
		Enabled: true, Lifecycle: "ready",
		ConfigDocument: json.RawMessage(`{"token":"Bearer sk-live"}`),
	})
	if !errors.Is(err, ErrConfigContainsSecret) {
		t.Fatalf("PutInstance(secretful) = %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "sk-live") {
		t.Fatalf("error echoed secret: %v", err)
	}
}

func openPluginSeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath:    library,
		DataDir:       filepath.Join(t.TempDir(), "seekdb-plugin"),
		Database:      seekdb.DefaultDatabase,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  16,
		MaxIdleConns:  8,
	}
	runtime, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open SeekDB plugin runtime: %v", err)
	}
	return runtime, runtime.SQL(), config
}

func reopenPluginSeekDB(t *testing.T, config seekdb.Config) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	runtime, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("reopen SeekDB plugin runtime: %v", err)
	}
	return runtime, runtime.SQL(), config
}

func reservePluginLoopback(t *testing.T) string {
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

func closePluginSeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close SeekDB plugin runtime: %v", err)
	}
}
