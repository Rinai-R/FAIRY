//go:build integration

package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fairy/coredb"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProductionRuntimeCompositionAndClose(t *testing.T) {
	databaseURL, cleanupSchema := isolatedRuntimeSchema(t, true)
	defer cleanupSchema()
	setRuntimeEnvironment(t, databaseURL, testMasterKey())

	root := t.TempDir()
	rt, err := Open(RuntimeOptions{ConfigRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Database == nil || rt.MemoryStore == nil || rt.Secret == nil {
		t.Fatalf("runtime dependencies = database:%v memory:%v secret:%v", rt.Database != nil, rt.MemoryStore != nil, rt.Secret != nil)
	}
	if !rt.Secret.Encrypted() {
		t.Fatal("runtime secret store is not encrypted")
	}
	if _, err := rt.MemoryStore.SummaryContext(context.Background()); err != nil {
		t.Fatalf("memory store does not share production pool: %v", err)
	}
	for _, path := range []string{"intelligence/fairy.sqlite3", "model/secrets.sqlite3"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("runtime created legacy SQLite path %s: %v", path, err)
		}
	}
	database := rt.Database
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if stats := database.Stats(); stats.TotalConns != 0 || stats.AcquiredConns != 0 || stats.IdleConns != 0 {
		t.Fatalf("closed pool stats = %#v", stats)
	}
}

func TestProductionRuntimeRejectsMissingRequiredDependencies(t *testing.T) {
	migratedURL, cleanupMigrated := isolatedRuntimeSchema(t, true)
	defer cleanupMigrated()
	unmigratedURL, cleanupUnmigrated := isolatedRuntimeSchema(t, false)
	defer cleanupUnmigrated()

	tests := []struct {
		name        string
		databaseURL string
		masterKey   string
		want        string
	}{
		{name: "database URL", masterKey: testMasterKey(), want: "FAIRY_DATABASE_URL is required"},
		{name: "schema", databaseURL: unmigratedURL, masterKey: testMasterKey(), want: "schema"},
		{name: "master key", databaseURL: migratedURL, want: "FAIRY_SECRET_MASTER_KEY is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRuntimeEnvironment(t, tt.databaseURL, tt.masterKey)
			root := t.TempDir()
			rt, err := Open(RuntimeOptions{ConfigRoot: root})
			if rt != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Open() = (%v, %v), want error containing %q", rt, err, tt.want)
			}
			for _, path := range []string{"intelligence/fairy.sqlite3", "model/secrets.sqlite3"} {
				if _, statErr := os.Stat(filepath.Join(root, path)); !os.IsNotExist(statErr) {
					t.Fatalf("failed startup created SQLite path %s: %v", path, statErr)
				}
			}
		})
	}
}

func isolatedRuntimeSchema(t *testing.T, migrate bool) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rawURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if rawURL == "" {
		rawURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fairy_runtime_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	databaseURL := withRuntimeSearchPath(t, rawURL, schema)
	if migrate {
		pool, err := coredb.Open(ctx, coredb.ShortTimeoutConfig(databaseURL))
		if err != nil {
			t.Fatal(err)
		}
		if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
			pool.Close()
			t.Fatal(err)
		}
		pool.Close()
	}
	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		pool, err := pgxpool.New(cleanupCtx, rawURL)
		if err != nil {
			t.Logf("open cleanup pool: %v", err)
			return
		}
		defer pool.Close()
		_, _ = pool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	}
	return databaseURL, cleanup
}

func withRuntimeSearchPath(t *testing.T, rawURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	values := parsed.Query()
	values.Set("search_path", schema)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func setRuntimeEnvironment(t *testing.T, databaseURL, masterKey string) {
	t.Helper()
	t.Setenv(coredb.EnvDatabaseURL, databaseURL)
	t.Setenv(coredb.EnvMaxConns, "4")
	t.Setenv(coredb.EnvMinConns, "0")
	t.Setenv(coredb.EnvConnectTimeout, "2s")
	t.Setenv(coredb.EnvQueryTimeout, "2s")
	t.Setenv("FAIRY_SECRET_MASTER_KEY", masterKey)
}

func testMasterKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}
