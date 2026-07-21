//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateAndVerifySchemaIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()

	if _, err := VerifySchema(ctx, pool); !errors.Is(err, ErrSchemaAbsent) {
		t.Fatalf("VerifySchema before migrate err = %v, want ErrSchemaAbsent", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if err != nil {
		t.Fatalf("VerifySchema after migrate error = %v", err)
	}
	if !status.Current || status.PresentObjects != status.ExpectedObjects {
		t.Fatalf("status = %#v", status)
	}
	for _, table := range schemaTableNames() {
		assertRegclass(t, ctx, pool.Raw(), table, true)
	}
	for _, index := range schemaIndexes {
		assertRegclass(t, ctx, pool.Raw(), index.Name, true)
	}
	assertRegclass(t, ctx, pool.Raw(), "fairy_schema_migrations", false)
	assertRegclass(t, ctx, pool.Raw(), "sqlite_import_runs", false)
	for _, column := range [][2]string{
		{"extraction_batches", "attempt_count"},
		{"knowledge_entries", "confidence_basis_points"},
		{"knowledge_sources", "rank"},
		{"memory_embedding_items", "dimensions"},
		{"secret_values", "key_version"},
	} {
		assertColumnType(t, ctx, pool.Raw(), column[0], column[1], "integer")
	}

	var hasTrgm bool
	if err := pool.Raw().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')").Scan(&hasTrgm); err != nil {
		t.Fatalf("checking pg_trgm: %v", err)
	}
	if !hasTrgm {
		t.Fatal("pg_trgm extension is not installed")
	}
}

func TestMigrateIsIdempotentIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('sentinel', 'character', 1, 1)"); err != nil {
		t.Fatalf("insert sentinel: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var count int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM conversations WHERE id = 'sentinel'").Scan(&count); err != nil {
		t.Fatalf("count sentinel: %v", err)
	}
	if count != 1 {
		t.Fatalf("sentinel count = %d, want 1", count)
	}
}

func TestMigrateSerializesConcurrentCallersIntegration(t *testing.T) {
	ctx := t.Context()
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
	status, err := VerifySchema(ctx, pool)
	if err != nil || !status.Current {
		t.Fatalf("VerifySchema() status = %#v, err = %v", status, err)
	}
}

func TestMigratePreservesDomainConstraintsIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	_, err := pool.Raw().Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('invalid', 'character', -1, -1)")
	if err == nil {
		t.Fatal("negative conversation timestamps must be rejected")
	}
	_, err = pool.Raw().Exec(ctx, "INSERT INTO secret_values(namespace, name, key_version, nonce, ciphertext, aad, created_at_ms, updated_at_ms) VALUES ('model', 'key', 1, $1, $2, 'model:key', 1, 1)", []byte("short"), []byte("ciphertext"))
	if err == nil {
		t.Fatal("invalid secret nonce length must be rejected")
	}
}

func TestVerifySchemaReportsPartialObjectsWithoutMutatingIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if _, err := pool.Raw().Exec(ctx, "CREATE TABLE conversations (id text PRIMARY KEY)"); err != nil {
		t.Fatalf("create partial table: %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifySchema() err = %v, want ErrSchemaNotCurrent", err)
	}
	if status.Current || len(status.MissingObjects) == 0 || status.MissingObjects[0] != "column:conversations.character_id" {
		t.Fatalf("partial status = %#v", status)
	}
	assertRegclass(t, ctx, pool.Raw(), "conversation_turns", false)
	if err := Migrate(ctx, pool); !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("Migrate(partial schema) err = %v, want ErrSchemaNotCurrent", err)
	}
}

func TestMigrateRejectsLegacyTablesIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, "CREATE TABLE fairy_schema_migrations (version integer PRIMARY KEY)"); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifySchema() err = %v, want ErrSchemaNotCurrent", err)
	}
	if len(status.UnexpectedObjects) != 1 || status.UnexpectedObjects[0] != "table:fairy_schema_migrations" {
		t.Fatalf("status = %#v", status)
	}
	if err := Migrate(ctx, pool); !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("Migrate(legacy schema) err = %v, want ErrSchemaNotCurrent", err)
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

func assertColumnType(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, column string, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT data_type FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&got); err != nil {
		t.Fatalf("reading %s.%s type: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("%s.%s type = %s, want %s", table, column, got, want)
	}
}
