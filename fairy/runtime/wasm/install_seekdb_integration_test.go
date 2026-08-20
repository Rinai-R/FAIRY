//go:build integration

package wasm

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fairy/plugin"
	"fairy/runtime/seekdb"
)

func TestRealSeekDBInstallerInstallsEnablesAndRollsBackFailedUpgrade(t *testing.T) {
	runtime, database, cfg := openInstallSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeInstallSeekDB(t, runtime, cfg.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatal(err)
	}
	store, err := plugin.NewStore(database, cfg.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	installer, err := NewInstaller(host, store)
	if err != nil {
		t.Fatal(err)
	}
	bundle := plugin.Bundle{
		Manifest: plugin.Manifest{
			SchemaVersion: 1, ID: "fairy.plugin.example", Version: "1.0.0",
			ABI: plugin.ABIRange{Min: 1, Max: 1}, Entry: plugin.EntryModule, Exports: plugin.RequiredExports(),
			ConfigSchemaVersion: 1, DataSchemaVersion: 1,
		},
		Module: echoGuestWASM(),
		SHA256: [32]byte{1},
	}
	if err := installer.Install(t.Context(), "echo-1", bundle); err != nil {
		t.Fatal(err)
	}
	installed, err := store.Instance(t.Context(), "echo-1")
	if err != nil || installed.Enabled || installed.Lifecycle != "disabled" {
		t.Fatalf("Install() instance = %#v, %v", installed, err)
	}
	if err := installer.Enable(t.Context(), "echo-1", bundle); err != nil {
		t.Fatal(err)
	}
	enabled, err := store.Instance(t.Context(), "echo-1")
	if err != nil || !enabled.Enabled || enabled.Lifecycle != "ready" || len(enabled.CapabilityGrants) != 0 {
		t.Fatalf("Enable() should not promote manifest declarations to grants: %#v, %v", enabled, err)
	}

	next := bundle
	next.Manifest.Version = "1.0.1"
	next.Module = growGuestWASM()
	next.SHA256 = [32]byte{2}
	if err := installer.Upgrade(t.Context(), "echo-1", next); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.Instance(t.Context(), "echo-1")
	if err != nil || upgraded.PluginVersion != "1.0.1" || !upgraded.Enabled {
		t.Fatalf("Upgrade() instance = %#v, %v", upgraded, err)
	}
	if err := installer.Disable(t.Context(), "echo-1"); err != nil {
		t.Fatal(err)
	}
	disabled, err := store.Instance(t.Context(), "echo-1")
	if err != nil || disabled.Enabled || disabled.Lifecycle != "disabled" {
		t.Fatalf("Disable() instance = %#v, %v", disabled, err)
	}

	bad := bundle
	bad.Manifest.Version = "1.0.2"
	bad.Module = emptyModule
	bad.SHA256 = [32]byte{3}
	if err := installer.Upgrade(t.Context(), "echo-1", bad); err == nil {
		t.Fatal("Upgrade(empty module) succeeded")
	}
	after, err := store.Instance(t.Context(), "echo-1")
	if err != nil || after.PluginVersion != "1.0.1" {
		t.Fatalf("failed upgrade changed instance: %#v, %v", after, err)
	}
	var rolled int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM plugin_upgrade_journal WHERE instance_id = 'echo-1' AND status = 'rolled_back'`).Scan(&rolled); err != nil {
		t.Fatal(err)
	}
	if rolled != 1 {
		t.Fatalf("rolled_back journal count = %d", rolled)
	}
}

func openInstallSeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath:    library,
		DataDir:       filepath.Join(t.TempDir(), "seekdb-install"),
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
		t.Fatalf("open SeekDB installer runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveInstallLoopback(t *testing.T) string {
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

func closeInstallSeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close SeekDB installer runtime: %v", err)
	}
}
