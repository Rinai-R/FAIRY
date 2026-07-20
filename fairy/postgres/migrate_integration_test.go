//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateAndVerifySchemaIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()

	if _, err := VerifySchema(ctx, pool, CurrentSchemaVersion); !errors.Is(err, ErrSchemaAbsent) {
		t.Fatalf("VerifySchema before migrate err = %v, want ErrSchemaAbsent", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	status, err := VerifySchema(ctx, pool, CurrentSchemaVersion)
	if err != nil {
		t.Fatalf("VerifySchema after migrate error = %v", err)
	}
	if !status.Current || status.AppliedVersion != CurrentSchemaVersion {
		t.Fatalf("status = %#v", status)
	}
	assertRegclass(t, ctx, pool.Raw(), "conversations", true)
	assertRegclass(t, ctx, pool.Raw(), "memory_embedding_vec", false)
	assertRegclass(t, ctx, pool.Raw(), "personal_memories_content_trgm", true)
	assertRegclass(t, ctx, pool.Raw(), "knowledge_entries_topic_trgm", true)
	assertRegclass(t, ctx, pool.Raw(), "memory_embedding_jobs_status", true)
	assertRegclass(t, ctx, pool.Raw(), "extraction_batches_one_running", true)
	assertRegclass(t, ctx, pool.Raw(), "secret_values", true)
	assertRegclass(t, ctx, pool.Raw(), "sqlite_import_runs", true)

	var hasTrgm bool
	if err := pool.Raw().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')").Scan(&hasTrgm); err != nil {
		t.Fatalf("checking pg_trgm: %v", err)
	}
	if !hasTrgm {
		t.Fatal("pg_trgm extension is not installed")
	}
}

func TestMigrateSerializesConcurrentCallersIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Migrate(ctx, pool)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Migrate() error = %v", err)
		}
	}
	var count int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM fairy_schema_migrations WHERE version = $1", CurrentSchemaVersion).Scan(&count); err != nil {
		t.Fatalf("counting migration rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration row count = %d, want 1", count)
	}
}

func TestMigrateChecksumMismatchIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	migrations[0].Checksum = strings.Repeat("0", 64)
	err = migrate(ctx, pool.Raw(), migrations)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("migrate with changed checksum err = %v, want checksum mismatch", err)
	}
}

func TestVerifySchemaBehindIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	_, err := pool.Raw().Exec(ctx, `
CREATE TABLE fairy_schema_migrations (
  version integer PRIMARY KEY CHECK (version > 0),
  name text NOT NULL,
  checksum_sha256 text NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
  applied_at timestamptz NOT NULL DEFAULT now()
)`)
	if err != nil {
		t.Fatalf("creating empty migrations table: %v", err)
	}
	status, err := VerifySchema(ctx, pool, CurrentSchemaVersion)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifySchema err = %v, want ErrSchemaNotCurrent", err)
	}
	if status.AppliedVersion != 0 || status.Current {
		t.Fatalf("status = %#v", status)
	}
}

func openIsolatedPool(t *testing.T, ctx context.Context) *Pool {
	t.Helper()
	databaseURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	adminConfig := ShortTimeoutConfig(databaseURL)
	admin, err := pgxpool.New(ctx, adminConfig.URL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("fairy_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanupPool, err := pgxpool.New(cleanupCtx, adminConfig.URL)
		if err != nil {
			t.Logf("open cleanup pool: %v", err)
			return
		}
		defer cleanupPool.Close()
		_, _ = cleanupPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	config := ShortTimeoutConfig(withSearchPath(t, databaseURL, schema))
	pool, err := Open(ctx, config)
	if err != nil {
		t.Fatalf("open isolated pool: %v", err)
	}
	return pool
}

func withSearchPath(t *testing.T, rawURL string, schema string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	values := parsed.Query()
	values.Set("search_path", schema)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func assertRegclass(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, want bool) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("checking regclass %s: %v", name, err)
	}
	if exists != want {
		t.Fatalf("to_regclass(%s) exists = %v, want %v", name, exists, want)
	}
}
