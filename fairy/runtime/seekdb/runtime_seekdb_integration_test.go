//go:build integration

package seekdb

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRealSeekDBPersistsCommittedTransactionAcrossRestart(t *testing.T) {
	binary := os.Getenv(EnvBinaryPath)
	if binary == "" {
		t.Skip(EnvBinaryPath + " is not set")
	}
	address := reserveLoopbackAddress(t)
	config := Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-data"),
		Address:       address,
		Database:      DefaultDatabase,
		User:          DefaultUser,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  4,
		MaxIdleConns:  2,
	}

	first, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	queryCtx, cancel := first.QueryContext(t.Context())
	if _, err := first.SQL().ExecContext(queryCtx, `
CREATE TABLE IF NOT EXISTS fairy_seekdb_runtime_probe (
  id BIGINT PRIMARY KEY,
  payload VARCHAR(128) NOT NULL
)`); err != nil {
		cancel()
		closeRuntimeForIntegrationTest(t, first, config.ShutdownLimit)
		t.Fatal(err)
	}
	tx, err := first.SQL().BeginTx(queryCtx, nil)
	if err == nil {
		_, err = tx.ExecContext(queryCtx, `INSERT INTO fairy_seekdb_runtime_probe(id, payload) VALUES (?, ?)`, 1, "persisted-by-seekdb")
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	cancel()
	if err != nil {
		closeRuntimeForIntegrationTest(t, first, config.ShutdownLimit)
		t.Fatal(err)
	}
	closeRuntimeForIntegrationTest(t, first, config.ShutdownLimit)

	second, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRuntimeForIntegrationTest(t, second, config.ShutdownLimit)
	queryCtx, cancel = second.QueryContext(t.Context())
	defer cancel()
	var payload string
	if err := second.SQL().QueryRowContext(queryCtx, `SELECT payload FROM fairy_seekdb_runtime_probe WHERE id = ?`, 1).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != "persisted-by-seekdb" {
		t.Fatalf("payload = %q", payload)
	}
}

func TestRealSeekDBEmbeddedRuntimeLeaseSurvivesRepeatedSchemaVerification(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	migrations := BuiltinMigrations()
	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(embedded client lease) error = %v", err)
	}
	current := migrations[len(migrations)-1]
	deadline := time.Now().Add(16 * time.Second)
	for iteration := 1; time.Now().Before(deadline); iteration++ {
		connection, err := database.Conn(t.Context())
		if err != nil {
			t.Fatalf("embedded client lease iteration %d acquire connection: %v", iteration, err)
		}
		if err := current.Verify(t.Context(), connection); err != nil {
			connection.Close()
			t.Fatalf("embedded client lease iteration %d current schema Verify: %v", iteration, err)
		}
		connection.Close()
		if err := database.PingContext(t.Context()); err != nil {
			t.Fatalf("embedded client lease iteration %d Ping: %v", iteration, err)
		}
		select {
		case <-instance.Done():
			t.Fatalf("embedded client lease iteration %d runtime exited: %v", iteration, instance.Err())
		default:
		}
	}
}

func reserveLoopbackAddress(t *testing.T) string {
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

func closeRuntimeForIntegrationTest(t *testing.T, runtime *Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
