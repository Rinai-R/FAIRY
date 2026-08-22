package seekdb

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedPreparedStatementValidatesLifecycleWithoutOpeningEngine(t *testing.T) {
	connection := &embedConn{}
	if _, err := connection.Prepare(""); err == nil {
		t.Fatal("Prepare(empty) error = nil")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := connection.PrepareContext(cancelled, "SELECT ?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareContext(cancelled) error = %v, want context.Canceled", err)
	}

	statement, err := connection.Prepare("SELECT ?")
	if err != nil {
		t.Fatal(err)
	}
	if got := statement.NumInput(); got != -1 {
		t.Fatalf("NumInput() = %d, want -1", got)
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := statement.Exec([]driver.Value{"value"}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Exec() after Close error = %v, want closed statement", err)
	}
}

func TestNamedValuesPreserveOrder(t *testing.T) {
	values := namedValues([]driver.Value{"first", int64(2)})
	if len(values) != 2 || values[0].Ordinal != 1 || values[0].Value != "first" || values[1].Ordinal != 2 || values[1].Value != int64(2) {
		t.Fatalf("namedValues() = %#v", values)
	}
}

func TestOpenSQLDatabaseAppliesPoolLimit(t *testing.T) {
	config := testRuntimeConfig(t)
	config.MaxOpenConns = 7
	config.MaxIdleConns = 3
	database, err := openSQLDatabase(config)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if got := database.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections = %d", got)
	}
}

func TestRuntimeExposesSQLAndBoundedQueryContext(t *testing.T) {
	config := testRuntimeConfig(t)
	config.QueryLimit = 50 * time.Millisecond
	runtime, err := open(t.Context(), config, testLaunchOptions())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.SQL() == nil {
		t.Fatal("SQL() = nil")
	}
	queryCtx, cancel := runtime.QueryContext(t.Context())
	defer cancel()
	deadline, ok := queryCtx.Deadline()
	if !ok || time.Until(deadline) > config.QueryLimit {
		t.Fatalf("QueryContext() deadline = %v, ok = %t", deadline, ok)
	}
	closeCtx, closeCancel := context.WithTimeout(t.Context(), time.Second)
	defer closeCancel()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if runtime.SQL() != nil {
		t.Fatal("SQL() remained available after Close")
	}
}

func TestRuntimeErrorRedactsLibraryAndDataPaths(t *testing.T) {
	config := testRuntimeConfig(t)
	config.LibraryPath = "/private/seekdb-library/libseekdb.dylib"
	err := redactRuntimeError(config, errors.New("load failed from /private/seekdb-library/libseekdb.dylib in "+config.DataDir))
	if strings.Contains(err.Error(), config.LibraryPath) || strings.Contains(err.Error(), config.DataDir) || !strings.Contains(err.Error(), "[seekdb-library]") || !strings.Contains(err.Error(), "[seekdb-data]") {
		t.Fatalf("redactRuntimeError() = %v", err)
	}
}
