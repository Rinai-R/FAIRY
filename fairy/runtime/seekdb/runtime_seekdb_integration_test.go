//go:build integration

package seekdb

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealSeekDBPersistsCommittedTransactionAcrossRestart(t *testing.T) {
	library := os.Getenv(EnvLibrary)
	if library == "" {
		t.Skip(EnvLibrary + " is not set")
	}
	before := seekDBProcessSnapshot(t)
	config := Config{
		LibraryPath:   library,
		DataDir:       processIntegrationDataDir(t),
		Database:      uniqueIntegrationDatabase(t),
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
	assertNoSeekDBChildProcess(t, before)
	assertNoSQLListenPort(t)
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
	assertNoSeekDBChildProcess(t, before)
	assertNoSQLListenPort(t)
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

func TestRealSeekDBAcceptsFoundationAsciiCharsetDDL(t *testing.T) {
	library := os.Getenv(EnvLibrary)
	if library == "" {
		t.Skip(EnvLibrary + " is not set")
	}
	config := Config{
		LibraryPath:   library,
		DataDir:       processIntegrationDataDir(t),
		Database:      uniqueIntegrationDatabase(t),
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  4,
		MaxIdleConns:  2,
	}
	runtime, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRuntimeForIntegrationTest(t, runtime, config.ShutdownLimit)
	queryCtx, cancel := runtime.QueryContext(t.Context())
	defer cancel()
	if _, err := runtime.SQL().ExecContext(queryCtx, `
CREATE TABLE config_documents_charset_probe (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  PRIMARY KEY (id)
)`); err != nil {
		t.Fatal(err)
	}
	var collation string
	if err := runtime.SQL().QueryRowContext(queryCtx, `
SELECT collation_name
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND column_name = ?`, "config_documents_charset_probe", "id").Scan(&collation); err != nil {
		t.Fatal(err)
	}
	if !schemaCollationsEqual("ascii_bin", strings.ToLower(collation)) {
		t.Fatalf("collation = %q", collation)
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

func seekDBProcessSnapshot(t *testing.T) map[string]struct{} {
	t.Helper()
	output, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	found := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if filepath.Base(fields[len(fields)-1]) == "seekdb" {
			found[fields[0]] = struct{}{}
		}
	}
	return found
}

func assertNoSeekDBChildProcess(t *testing.T, before map[string]struct{}) {
	t.Helper()
	after := seekDBProcessSnapshot(t)
	for pid := range after {
		if _, existed := before[pid]; !existed {
			t.Fatalf("SeekDB opened a seekdb child process pid=%s", pid)
		}
	}
}

func assertNoSQLListenPort(t *testing.T) {
	t.Helper()
	output, err := exec.Command("lsof", "-nP", "-iTCP:2881", "-sTCP:LISTEN").CombinedOutput()
	if err != nil {
		if len(bytes.TrimSpace(output)) == 0 {
			return
		}
	}
	if len(bytes.TrimSpace(output)) > 0 {
		t.Fatalf("SeekDB SQL TCP port 2881 is listening:\n%s", output)
	}
}

func closeRuntimeForIntegrationTest(t *testing.T, runtime *Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
