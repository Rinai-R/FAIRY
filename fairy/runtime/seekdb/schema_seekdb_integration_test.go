//go:build integration

package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestRealSeekDBFoundationSchemaRecoversPartialAndPersists(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	closed := false
	defer func() {
		if !closed {
			closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
		}
	}()
	database := instance.SQL()

	// Simulate a process exit after the first implicitly committed DDL but
	// before the revision journal was committed. The builtin migration must
	// accept that exact table and create the remaining five.
	if _, err := database.ExecContext(t.Context(), foundationSchema[0].ddl); err != nil {
		t.Fatalf("precreate partial foundation schema: %v", err)
	}
	if got := integrationTableCount(t, database, foundationSchema[0].name); got != 1 {
		t.Fatalf("precreated table count = %d, want 1", got)
	}

	if err := MigrateSchema(t.Context(), database, BuiltinMigrations()); err != nil {
		t.Fatalf("MigrateSchema(partial foundation) error = %v", err)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertFoundationSchemaVerified(t, database)
	assertFoundationTableSet(t, database)
	if got := integrationJournalRowForRevision(t, database, foundationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("foundation attempt count = %d, want 1", got)
	}

	if err := MigrateSchema(t.Context(), database, BuiltinMigrations()); err != nil {
		t.Fatalf("idempotent MigrateSchema(foundation) error = %v", err)
	}
	if got := integrationJournalRowForRevision(t, database, foundationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("foundation attempt count after repeat = %d, want 1", got)
	}
	assertFoundationChecksRejectInvalidData(t, database)

	closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	closed = true

	restarted, err := Open(t.Context(), config)
	if err != nil {
		t.Fatalf("restart SeekDB foundation runtime: %v", err)
	}
	instance = restarted
	closed = false
	assertCurrentSchema(t, restarted.SQL(), CurrentSchemaRevision())
	assertFoundationSchemaVerified(t, restarted.SQL())
	if got := integrationJournalRowForRevision(t, restarted.SQL(), foundationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("foundation attempt count after restart = %d, want 1", got)
	}
}

func TestRealSeekDBFoundationSchemaRejectsShapeDrift(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()

	if _, err := database.ExecContext(t.Context(), `
CREATE TABLE config_documents (
  namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  document_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  PRIMARY KEY (namespace, document_key)
)`); err != nil {
		t.Fatal(err)
	}
	err := MigrateSchema(t.Context(), database, BuiltinMigrations())
	if err == nil {
		t.Fatal("MigrateSchema accepted a pre-existing table with an invalid shape")
	}
	row := integrationJournalRowForRevision(t, database, foundationSchemaRevision)
	if row.State != string(MigrationFailed) || row.ErrorCode != "VERIFY_FAILED" || row.AttemptCount != 1 {
		t.Fatalf("shape-drift journal = %#v", row)
	}
	status, readinessErr := CheckSchema(t.Context(), database, CurrentSchemaRevision())
	if !errors.Is(readinessErr, ErrSchemaNotCurrent) || status.State != SchemaNotCurrent {
		t.Fatalf("shape-drift readiness = %#v, error = %v", status, readinessErr)
	}
}

func TestRealSeekDBFoundationSchemaRejectsSameNameWeakCheck(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()

	if _, err := database.ExecContext(t.Context(), `
CREATE TABLE owner_identities (
  namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  subject_digest BINARY(32) NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (namespace, subject_digest),
  KEY owner_identities_created_idx (created_at_ms),
  CONSTRAINT owner_identities_invariants_check CHECK (created_at_ms >= 0)
)`); err != nil {
		t.Fatal(err)
	}
	assertFoundationMigrationVerifyFailure(t, database, "same-name weak CHECK")
}

func TestRealSeekDBFoundationSchemaRejectsMissingOrWrongForeignKey(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mutateDDL func(string) string
	}{
		{
			name: "missing",
			mutateDDL: func(ddl string) string {
				return strings.Replace(ddl, `  CONSTRAINT plugin_instances_package_fk FOREIGN KEY (plugin_id, plugin_version)
    REFERENCES plugin_packages (plugin_id, version) ON UPDATE RESTRICT ON DELETE RESTRICT,
`, "", 1)
			},
		},
		{
			name: "wrong update rule",
			mutateDDL: func(ddl string) string {
				return strings.Replace(ddl, "ON UPDATE RESTRICT ON DELETE RESTRICT", "ON UPDATE NO ACTION ON DELETE RESTRICT", 1)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			instance, config := openSchemaMigrationRuntime(t)
			defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
			database := instance.SQL()
			if _, err := database.ExecContext(t.Context(), foundationSchema[4].ddl); err != nil {
				t.Fatalf("precreate plugin_packages: %v", err)
			}
			mutated := testCase.mutateDDL(foundationSchema[5].ddl)
			if mutated == foundationSchema[5].ddl {
				t.Fatal("test setup did not mutate plugin_instances DDL")
			}
			if _, err := database.ExecContext(t.Context(), mutated); err != nil {
				t.Fatalf("precreate plugin_instances: %v", err)
			}
			assertFoundationMigrationVerifyFailure(t, database, testCase.name+" foreign key")
		})
	}
}

func assertFoundationMigrationVerifyFailure(t *testing.T, database *sql.DB, drift string) {
	t.Helper()
	err := MigrateSchema(t.Context(), database, BuiltinMigrations())
	if err == nil {
		t.Fatalf("MigrateSchema accepted %s", drift)
	}
	t.Logf("MigrateSchema rejected %s: %v", drift, err)
	row := integrationJournalRowForRevision(t, database, foundationSchemaRevision)
	if row.State != string(MigrationFailed) || row.ErrorCode != "VERIFY_FAILED" || row.AttemptCount != 1 {
		t.Fatalf("%s journal = %#v", drift, row)
	}
}

func assertFoundationSchemaVerified(t *testing.T, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := BuiltinMigrations()[0].Verify(t.Context(), connection); err != nil {
		t.Fatalf("verify foundation schema: %v", err)
	}
}

func assertFoundationTableSet(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, table := range foundationSchema {
		if got := integrationTableCount(t, database, table.name); got != 1 {
			t.Errorf("foundation table %s count = %d, want 1", table.name, got)
		}
	}
	var businessTableCount int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name <> 'schema_revisions'`).Scan(&businessTableCount); err != nil {
		t.Fatal(err)
	}
	if businessTableCount != len(foundationSchema) {
		t.Fatalf("business table count = %d, want %d", businessTableCount, len(foundationSchema))
	}
}

func assertFoundationChecksRejectInvalidData(t *testing.T, database *sql.DB) {
	t.Helper()
	nonce := make([]byte, 12)
	digest := sha256.Sum256([]byte("verified plugin package"))
	validManifest := `{"id":"fairy.test","version":"1.0.0"}`

	expectSeekDBConstraintError(t, database, `
INSERT INTO config_documents(namespace, document_key, schema_version, revision, document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "runtime", "model", 1, 1, `[]`, 1, 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO secret_values(namespace, name, key_version, nonce, ciphertext, aad, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "model", "primary", 1, nonce[:11], []byte("ciphertext"), "model/primary", 1, 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO owner_identities(namespace, subject_digest, created_at_ms)
VALUES (?, ?, ?)`, "qq", digest[:], 0)
	expectSeekDBConstraintError(t, database, `
	INSERT INTO characters(character_id, revision, name, snapshot, appearance_ref, created_at_ms, updated_at_ms)
	VALUES (?, ?, ?, ?, ?, ?, ?)`, "atri", 0, "亚托莉", `{}`, nil, 1, 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO plugin_packages(plugin_id, version, abi_version, artifact_sha256, publisher_digest, manifest, verified_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "fairy.test", "1.0.0", 1, digest[:], nil, `[]`, 1)

	if _, err := database.ExecContext(t.Context(), `
INSERT INTO plugin_packages(plugin_id, version, abi_version, artifact_sha256, publisher_digest, manifest, verified_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "fairy.test", "1.0.0", 1, digest[:], nil, validManifest, 1); err != nil {
		t.Fatalf("insert valid plugin package: %v", err)
	}
	expectSeekDBConstraintError(t, database, `
INSERT INTO plugin_instances(instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "qq-main", "fairy.test", "1.0.0", 0, "ready", `[]`, `{}`, 1, 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO plugin_instances(instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "qq-disabled-enabled", "fairy.test", "1.0.0", 1, "disabled", `[]`, `{}`, 1, 1)
	for _, row := range []struct {
		instanceID string
		enabled    int
		state      string
	}{
		{instanceID: "qq-disabled", enabled: 0, state: "disabled"},
		{instanceID: "qq-ready", enabled: 1, state: "ready"},
	} {
		if _, err := database.ExecContext(t.Context(), `
INSERT INTO plugin_instances(instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.instanceID, "fairy.test", "1.0.0", row.enabled, row.state, `[]`, `{}`, 1, 1); err != nil {
			t.Fatalf("insert valid plugin instance %s: %v", row.instanceID, err)
		}
	}
}

func expectSeekDBConstraintError(t *testing.T, database *sql.DB, statement string, arguments ...any) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), statement, arguments...); err == nil {
		t.Fatalf("SeekDB accepted row rejected by named CHECK: %s", normalizeDDL(statement))
	}
}

func verifyFoundationWithContext(ctx context.Context, database *sql.DB) error {
	connection, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	return verifyFoundationSchema(ctx, connection)
}
