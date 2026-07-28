//go:build integration

package coredb

import (
	"context"
	"crypto/sha256"
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
	gormschema "gorm.io/gorm/schema"
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
		assertRegclass(t, ctx, pool, table, true)
	}
	for _, model := range schemaModels() {
		parsed, err := gormschema.Parse(model, &sync.Map{}, gormschema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse schema model %T: %v", model, err)
		}
		for name := range parsed.ParseCheckConstraints() {
			assertConstraint(t, ctx, pool, parsed.Table, name, true)
		}
		for _, index := range parsed.ParseIndexes() {
			assertRegclass(t, ctx, pool, index.Name, true)
		}
	}
	for _, constraint := range postgresForeignKeys {
		assertConstraint(t, ctx, pool, constraint.Table, constraint.Name, true)
	}
	for _, index := range postgresIndexes {
		assertRegclass(t, ctx, pool, index.Name, true)
	}
	assertRegclass(t, ctx, pool, "fairy_schema_migrations", false)
	assertRegclass(t, ctx, pool, "sqlite_import_runs", false)
	for _, column := range [][2]string{
		{"extraction_batches", "attempt_count"},
		{"knowledge_entries", "confidence_basis_points"},
		{"knowledge_sources", "rank"},
		{"memory_embedding_items", "dimensions"},
		{"secret_values", "key_version"},
	} {
		assertColumnType(t, ctx, pool, column[0], column[1], "integer")
	}

	var hasTrgm bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')").Scan(&hasTrgm); err != nil {
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
	if _, err := pool.Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('sentinel', 'character', 1, 1)"); err != nil {
		t.Fatalf("insert sentinel: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM conversations WHERE id = 'sentinel'").Scan(&count); err != nil {
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
	_, err := pool.Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('invalid', 'character', -1, -1)")
	if err == nil {
		t.Fatal("negative conversation timestamps must be rejected")
	}
	_, err = pool.Exec(ctx, "INSERT INTO secret_values(namespace, name, key_version, nonce, ciphertext, aad, created_at_ms, updated_at_ms) VALUES ('model', 'key', 1, $1, $2, 'model:key', 1, 1)", []byte("short"), []byte("ciphertext"))
	if err == nil {
		t.Fatal("invalid secret nonce length must be rejected")
	}
	if _, err := pool.Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('constraint-conversation', 'character', 1, 1)"); err != nil {
		t.Fatalf("insert constraint conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('invalid-status-turn', 'constraint-conversation', 1, 'unknown', 'user', 'pending', 1, 1)
`); err == nil {
		t.Fatal("invalid turn status must be rejected")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('constraint-turn', 'constraint-conversation', 1, 'completed', 'user', 'pending', 1, 1)
`); err != nil {
		t.Fatalf("insert constraint turn: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms)
VALUES ('invalid-parts-message', 'constraint-conversation', 'constraint-turn', 1, 'assistant', 'text', '{}'::jsonb, 1)
`); err == nil {
		t.Fatal("non-array expression parts must be rejected")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO tool_executions(id, conversation_id, turn_id, call_id, tool_name, status, deadline_at_ms, created_at_ms, updated_at_ms)
VALUES ('invalid-completed-tool', 'constraint-conversation', 'constraint-turn', 'call', 'desktop_observe', 'completed', 2, 1, 1)
`); err == nil {
		t.Fatal("completed tool execution without result fields must be rejected")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO stickers(id, content_sha256, mime_type, byte_count, content, description, tags, status, created_at_ms, updated_at_ms)
VALUES ('sticker-1', repeat('a', 64), 'image/png', 1, '\x01'::bytea, '', '[]'::jsonb, 'draft', 1, 1)
`); err != nil {
		t.Fatalf("insert first sticker hash: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO stickers(id, content_sha256, mime_type, byte_count, content, description, tags, status, created_at_ms, updated_at_ms)
VALUES ('sticker-2', repeat('a', 64), 'image/png', 1, '\x02'::bytea, '', '[]'::jsonb, 'draft', 1, 1)
`); err == nil {
		t.Fatal("duplicate sticker hash must be rejected")
	}
}

func TestVerifySchemaReportsPartialObjectsWithoutMutatingIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if _, err := pool.Exec(ctx, "CREATE TABLE conversations (id text PRIMARY KEY)"); err != nil {
		t.Fatalf("create partial table: %v", err)
	}
	fingerprintBefore := catalogFingerprint(t, ctx, pool)
	status, err := VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaAbsent) {
		t.Fatalf("VerifySchema() err = %v, want ErrSchemaAbsent", err)
	}
	if status.Current || len(status.MissingObjects) == 0 || status.MissingObjects[0] != "table:fairy_schema_state" {
		t.Fatalf("partial status = %#v", status)
	}
	if fingerprintAfter := catalogFingerprint(t, ctx, pool); fingerprintAfter != fingerprintBefore {
		t.Fatalf("VerifySchema mutated partial catalog: before %s, after %s", fingerprintBefore, fingerprintAfter)
	}
	assertRegclass(t, ctx, pool, "conversation_turns", false)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate(partial schema) error = %v", err)
	}
	status, err = VerifySchema(ctx, pool)
	if err != nil || !status.Current {
		t.Fatalf("VerifySchema() after additive migrate status = %#v, err = %v", status, err)
	}
}

func TestMigrateUpgradesPreviousStickerSchemaAndPreservesRowsIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('upgrade-conversation', 'character', 1, 1);
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('upgrade-turn', 'upgrade-conversation', 1, 'completed', 'user', 'pending', 1, 1);
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms)
VALUES ('upgrade-message', 'upgrade-conversation', 'upgrade-turn', 1, 'assistant', 'sentinel', '[]'::jsonb, 1);
DELETE FROM fairy_schema_state WHERE id = 1;
ALTER TABLE conversations ADD CONSTRAINT previous_conversations_character_check CHECK (character_id <> '');
ALTER TABLE conversation_messages DROP CONSTRAINT conversation_messages_invariants_check;
ALTER TABLE conversation_messages DROP COLUMN expression_parts;
DROP TABLE stickers;
`); err != nil {
		t.Fatalf("prepare previous sticker schema: %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifySchema() err = %v, want ErrSchemaNotCurrent", err)
	}
	if status.Current {
		t.Fatalf("previous schema unexpectedly current: %#v", status)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate(previous schema) error = %v", err)
	}
	status, err = VerifySchema(ctx, pool)
	if err != nil || !status.Current {
		t.Fatalf("VerifySchema() after upgrade status = %#v, err = %v", status, err)
	}
	var content string
	var expressionParts string
	if err := pool.QueryRow(ctx, `
SELECT content, expression_parts::text
FROM conversation_messages
WHERE id = 'upgrade-message'
`).Scan(&content, &expressionParts); err != nil {
		t.Fatalf("read preserved message: %v", err)
	}
	if content != "sentinel" || expressionParts != "[]" {
		t.Fatalf("preserved message content = %q, expression_parts = %q", content, expressionParts)
	}
	assertRegclass(t, ctx, pool, "stickers", true)
	assertConstraint(t, ctx, pool, "conversations", "previous_conversations_character_check", true)
}

func TestMigrateAndVerifyAllowUnrelatedTablesIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE operator_notes (id integer PRIMARY KEY, note text NOT NULL)"); err != nil {
		t.Fatalf("create unrelated table: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO operator_notes(id, note) VALUES (1, 'keep me')"); err != nil {
		t.Fatalf("insert unrelated row: %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if err != nil || !status.Current {
		t.Fatalf("VerifySchema() with unrelated table status = %#v, err = %v", status, err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() with unrelated table error = %v", err)
	}
	var note string
	if err := pool.QueryRow(ctx, "SELECT note FROM operator_notes WHERE id = 1").Scan(&note); err != nil {
		t.Fatalf("read unrelated row: %v", err)
	}
	if note != "keep me" {
		t.Fatalf("unrelated row = %q, want keep me", note)
	}
}

func TestMigrateFailsWhenExistingRowsViolateCurrentConstraintIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
DELETE FROM fairy_schema_state WHERE id = 1;
ALTER TABLE conversations DROP CONSTRAINT conversations_invariants_check;
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('invalid-existing-row', 'character', -1, -1);
`); err != nil {
		t.Fatalf("prepare invalid existing row: %v", err)
	}
	err := Migrate(ctx, pool)
	if err == nil {
		t.Fatal("Migrate() with invalid existing row succeeded")
	}
	if !strings.Contains(err.Error(), "conversations_invariants_check") {
		t.Fatalf("Migrate() error = %v, want constraint name", err)
	}
	assertConstraint(t, ctx, pool, "conversations", "conversations_invariants_check", false)
	status, verifyErr := VerifySchema(ctx, pool)
	if !errors.Is(verifyErr, ErrSchemaNotCurrent) || status.Current {
		t.Fatalf("VerifySchema() after failed migration status = %#v, err = %v", status, verifyErr)
	}
}

func TestVerifySchemaRejectsMissingAndOldRevisionIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE fairy_schema_state SET revision = 'previous' WHERE id = 1"); err != nil {
		t.Fatalf("set previous revision: %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) || status.Current || status.PresentObjects != 1 {
		t.Fatalf("VerifySchema() with previous revision status = %#v, err = %v", status, err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM fairy_schema_state WHERE id = 1"); err != nil {
		t.Fatalf("delete schema revision: %v", err)
	}
	status, err = VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) || status.Current || status.PresentObjects != 0 {
		t.Fatalf("VerifySchema() without revision status = %#v, err = %v", status, err)
	}
}

func TestVerifySchemaFailsWhenPoolIsClosedIntegration(t *testing.T) {
	pool := openIsolatedPool(t, t.Context())
	pool.Close()
	if _, err := VerifySchema(t.Context(), pool); err == nil {
		t.Fatal("VerifySchema on a closed pool succeeded")
	}
}

func openIsolatedPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
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
	database, err := Open(ctx, config)
	if err != nil {
		t.Fatalf("open isolated pool: %v", err)
	}
	return database.Raw()
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

func assertConstraint(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, name string, want bool) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS (
	SELECT 1
	FROM pg_constraint c
	JOIN pg_class t ON t.oid = c.conrelid
	JOIN pg_namespace n ON n.oid = t.relnamespace
	WHERE n.nspname = current_schema() AND t.relname = $1 AND c.conname = $2
)`, table, name).Scan(&exists); err != nil {
		t.Fatalf("checking constraint %s.%s: %v", table, name, err)
	}
	if exists != want {
		t.Fatalf("constraint %s.%s exists = %v, want %v", table, name, exists, want)
	}
}

func catalogFingerprint(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(ctx, testSchemaCatalogQuery)
	if err != nil {
		t.Fatalf("read catalog fingerprint: %v", err)
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var kind, owner, name string
		if err := rows.Scan(&kind, &owner, &name); err != nil {
			t.Fatalf("scan catalog fingerprint: %v", err)
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\n", kind, owner, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate catalog fingerprint: %v", err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

const testSchemaCatalogQuery = `
SELECT 'table' AS kind, tablename AS owner, '' AS name
FROM pg_tables
WHERE schemaname = current_schema()
UNION ALL
SELECT 'column', table_name, column_name
FROM information_schema.columns
WHERE table_schema = current_schema()
UNION ALL
SELECT 'constraint', t.relname, c.conname
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
UNION ALL
SELECT 'index', '', indexname
FROM pg_indexes
WHERE schemaname = current_schema()
ORDER BY 1, 2, 3`
