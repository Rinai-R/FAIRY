package seekdb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
