//go:build integration

package runtime

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

	pgstore "fairy/postgres"
	"fairy/vectorindex"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProductionRuntimeCompositionAndClose(t *testing.T) {
	databaseURL, cleanupSchema := isolatedRuntimeSchema(t, true)
	defer cleanupSchema()
	qdrantURL := testQdrantURL()
	ensureRuntimeCollection(t, qdrantURL)
	setRuntimeEnvironment(t, databaseURL, qdrantURL, testMasterKey())

	root := t.TempDir()
	rt, err := Open(Options{ConfigRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Database == nil || rt.MemoryStore == nil || rt.Secret == nil || rt.VectorIndex == nil {
		t.Fatalf("runtime dependencies = database:%v memory:%v secret:%v vector:%v", rt.Database != nil, rt.MemoryStore != nil, rt.Secret != nil, rt.VectorIndex != nil)
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
	qdrantURL := testQdrantURL()
	ensureRuntimeCollection(t, qdrantURL)
	migratedURL, cleanupMigrated := isolatedRuntimeSchema(t, true)
	defer cleanupMigrated()
	unmigratedURL, cleanupUnmigrated := isolatedRuntimeSchema(t, false)
	defer cleanupUnmigrated()

	tests := []struct {
		name        string
		databaseURL string
		qdrantURL   string
		masterKey   string
		want        string
	}{
		{name: "database URL", qdrantURL: qdrantURL, masterKey: testMasterKey(), want: "FAIRY_DATABASE_URL is required"},
		{name: "schema", databaseURL: unmigratedURL, qdrantURL: qdrantURL, masterKey: testMasterKey(), want: "schema"},
		{name: "qdrant URL", databaseURL: migratedURL, masterKey: testMasterKey(), want: "FAIRY_QDRANT_URL is required"},
		{name: "master key", databaseURL: migratedURL, qdrantURL: qdrantURL, want: "FAIRY_SECRET_MASTER_KEY is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRuntimeEnvironment(t, tt.databaseURL, tt.qdrantURL, tt.masterKey)
			root := t.TempDir()
			rt, err := Open(Options{ConfigRoot: root})
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
		pool, err := pgstore.Open(ctx, pgstore.ShortTimeoutConfig(databaseURL))
		if err != nil {
			t.Fatal(err)
		}
		if err := pgstore.Migrate(ctx, pool); err != nil {
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

func ensureRuntimeCollection(t *testing.T, rawURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := vectorindex.Open(ctx, vectorindex.Config{URL: rawURL, Timeout: 5 * time.Second, CollectionName: vectorindex.CollectionName})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.MigrateCollection(ctx); err != nil {
		t.Fatal(err)
	}
}

func setRuntimeEnvironment(t *testing.T, databaseURL, qdrantURL, masterKey string) {
	t.Helper()
	t.Setenv(pgstore.EnvDatabaseURL, databaseURL)
	t.Setenv(pgstore.EnvMaxConns, "4")
	t.Setenv(pgstore.EnvMinConns, "0")
	t.Setenv(pgstore.EnvConnectTimeout, "2s")
	t.Setenv(pgstore.EnvQueryTimeout, "2s")
	t.Setenv(vectorindex.EnvURL, qdrantURL)
	t.Setenv(vectorindex.EnvTimeout, "2s")
	t.Setenv("FAIRY_SECRET_MASTER_KEY", masterKey)
}

func testQdrantURL() string {
	if value := os.Getenv("FAIRY_TEST_QDRANT_GRPC_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:16334"
}

func testMasterKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}
