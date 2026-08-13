//go:build integration

package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRealSeekDBSchemaMigrationLifecycle(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()

	revisionOne := testRevision(1, "create fairy schema revision one probe")
	revisionTwo := testRevision(2, "create fairy schema revision two probe")
	revisionThree := testRevision(3, "create fairy schema revision three probe")
	migrationOne := integrationTableMigration(revisionOne, "create-revision-one-probe", "fairy_schema_revision_one_probe")
	migrationTwo := integrationTableMigration(revisionTwo, "create-revision-two-probe", "fairy_schema_revision_two_probe")

	if got := integrationTableCount(t, database, "schema_revisions"); got != 0 {
		t.Fatalf("schema journal table count before readiness = %d, want 0", got)
	}
	status, err := CheckSchema(t.Context(), database, revisionOne)
	if !errors.Is(err, ErrSchemaAbsent) {
		t.Fatalf("CheckSchema() before migration error = %v, want ErrSchemaAbsent", err)
	}
	if status.State != SchemaAbsent || status.Reason != "journal-absent" {
		t.Fatalf("CheckSchema() before migration status = %#v", status)
	}
	if got := integrationTableCount(t, database, "schema_revisions"); got != 0 {
		t.Fatalf("schema journal table count after readiness = %d, want 0", got)
	}

	if err := MigrateSchema(t.Context(), database, []Migration{migrationOne}); err != nil {
		t.Fatalf("MigrateSchema(revision 1) error = %v", err)
	}
	assertCurrentSchema(t, database, revisionOne)
	beforeReadiness := integrationJournalRowForRevision(t, database, revisionOne.Number)
	assertCurrentSchema(t, database, revisionOne)
	afterReadiness := integrationJournalRowForRevision(t, database, revisionOne.Number)
	if beforeReadiness != afterReadiness {
		t.Fatalf("readiness mutated schema journal: before = %#v, after = %#v", beforeReadiness, afterReadiness)
	}

	if err := MigrateSchema(t.Context(), database, []Migration{migrationOne}); err != nil {
		t.Fatalf("idempotent MigrateSchema(revision 1) error = %v", err)
	}
	revisionOneRow := integrationJournalRowForRevision(t, database, revisionOne.Number)
	if revisionOneRow.AttemptCount != 1 || revisionOneRow.State != string(MigrationCurrent) {
		t.Fatalf("revision 1 journal after idempotent retry = %#v", revisionOneRow)
	}

	injectedFailure := errors.New("injected migration apply failure")
	failingMigrationTwo := migrationTwo
	failingMigrationTwo.Apply = func(ctx context.Context, connection *sql.Conn) error {
		if _, err := connection.ExecContext(ctx, integrationCreateTableSQL("fairy_schema_revision_two_probe")); err != nil {
			return err
		}
		return injectedFailure
	}
	err = MigrateSchema(t.Context(), database, []Migration{migrationOne, failingMigrationTwo})
	if !errors.Is(err, injectedFailure) {
		t.Fatalf("failing MigrateSchema(revision 2) error = %v, want injected failure", err)
	}
	failedRevisionTwo := integrationJournalRowForRevision(t, database, revisionTwo.Number)
	if failedRevisionTwo.State != string(MigrationFailed) || failedRevisionTwo.AttemptCount != 1 || failedRevisionTwo.ErrorCode != "APPLY_FAILED" {
		t.Fatalf("failed revision 2 journal = %#v", failedRevisionTwo)
	}
	if got := integrationTableCount(t, database, "fairy_schema_revision_two_probe"); got != 1 {
		t.Fatalf("partially applied revision 2 table count = %d, want 1", got)
	}

	status, err = CheckSchema(t.Context(), database, revisionTwo)
	if !errors.Is(err, ErrSchemaNotCurrent) || status.State != SchemaNotCurrent || status.Observed == nil || status.Observed.State != MigrationFailed {
		t.Fatalf("CheckSchema(failed revision 2) status = %#v, error = %v", status, err)
	}
	status, err = CheckSchema(t.Context(), database, revisionOne)
	if !errors.Is(err, ErrSchemaAhead) || status.State != SchemaNotCurrent || status.Observed == nil || status.Observed.Revision.Number != revisionTwo.Number {
		t.Fatalf("CheckSchema(revision 1 behind failed revision 2) status = %#v, error = %v", status, err)
	}

	if err := MigrateSchema(t.Context(), database, []Migration{migrationOne, migrationTwo}); err != nil {
		t.Fatalf("recover MigrateSchema(revision 2) error = %v", err)
	}
	assertCurrentSchema(t, database, revisionTwo)
	recoveredRevisionTwo := integrationJournalRowForRevision(t, database, revisionTwo.Number)
	if recoveredRevisionTwo.State != string(MigrationCurrent) || recoveredRevisionTwo.AttemptCount != 2 || recoveredRevisionTwo.ErrorCode != "" {
		t.Fatalf("recovered revision 2 journal = %#v", recoveredRevisionTwo)
	}
	if got := integrationJournalRowForRevision(t, database, revisionOne.Number).AttemptCount; got != 1 {
		t.Fatalf("revision 1 attempt count after revision 2 recovery = %d, want 1", got)
	}

	checksumMismatch := migrationTwo
	checksumMismatch.Revision.Checksum = sha256.Sum256([]byte("changed revision two migration"))
	err = MigrateSchema(t.Context(), database, []Migration{migrationOne, checksumMismatch})
	if !errors.Is(err, ErrSchemaChecksumMismatch) {
		t.Fatalf("MigrateSchema(checksum mismatch) error = %v, want ErrSchemaChecksumMismatch", err)
	}
	if got := integrationJournalRowForRevision(t, database, revisionTwo.Number).AttemptCount; got != 2 {
		t.Fatalf("revision 2 attempt count after checksum mismatch = %d, want 2", got)
	}

	assertConcurrentMigrationLock(t, database, []Migration{migrationOne, migrationTwo}, revisionThree)
	assertCurrentSchema(t, database, revisionThree)
}

func openSchemaMigrationRuntime(t *testing.T) (*Runtime, Config) {
	t.Helper()
	binary := os.Getenv(EnvBinaryPath)
	if binary == "" {
		t.Skip(EnvBinaryPath + " is not set")
	}
	config := Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-schema-data"),
		Address:       reserveLoopbackAddress(t),
		Database:      DefaultDatabase,
		User:          DefaultUser,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  6,
		MaxIdleConns:  3,
	}
	instance, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return instance, config
}

func integrationTableMigration(revision Revision, name, table string) Migration {
	return Migration{
		Revision: revision,
		Name:     name,
		Apply: func(ctx context.Context, connection *sql.Conn) error {
			_, err := connection.ExecContext(ctx, integrationCreateTableSQL(table))
			return err
		},
		Verify: func(ctx context.Context, connection *sql.Conn) error {
			var count int64
			if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("table %s count = %d, want 1", table, count)
			}
			return nil
		},
	}
}

func integrationCreateTableSQL(table string) string {
	switch table {
	case "fairy_schema_revision_one_probe", "fairy_schema_revision_two_probe", "fairy_schema_revision_three_probe":
		return "CREATE TABLE IF NOT EXISTS " + table + " (id BIGINT NOT NULL PRIMARY KEY)"
	default:
		panic("unapproved integration table name: " + table)
	}
}

func integrationTableCount(t *testing.T, database *sql.DB, table string) int64 {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertCurrentSchema(t *testing.T, database *sql.DB, expected Revision) {
	t.Helper()
	status, err := CheckSchema(t.Context(), database, expected)
	if err != nil {
		t.Fatalf("CheckSchema(revision %d) error = %v", expected.Number, err)
	}
	if status.State != SchemaCurrent || status.Observed == nil || status.Observed.Revision != expected || status.Observed.State != MigrationCurrent {
		t.Fatalf("CheckSchema(revision %d) status = %#v", expected.Number, status)
	}
}

type integrationJournalRow struct {
	Checksum     string
	State        string
	AttemptCount int64
	StartedAtMS  int64
	FinishedAtMS sql.NullInt64
	ErrorCode    string
}

func integrationJournalRowForRevision(t *testing.T, database *sql.DB, revision int64) integrationJournalRow {
	t.Helper()
	var (
		row       integrationJournalRow
		checksum  []byte
		errorCode sql.NullString
	)
	if err := database.QueryRowContext(t.Context(), `
SELECT checksum, status, attempt_count, started_at_ms, finished_at_ms, error_code
FROM schema_revisions
WHERE revision = ?`, revision).Scan(
		&checksum,
		&row.State,
		&row.AttemptCount,
		&row.StartedAtMS,
		&row.FinishedAtMS,
		&errorCode,
	); err != nil {
		t.Fatal(err)
	}
	row.Checksum = string(checksum)
	if errorCode.Valid {
		row.ErrorCode = errorCode.String
	}
	return row
}

func assertConcurrentMigrationLock(t *testing.T, database *sql.DB, base []Migration, revision Revision) {
	t.Helper()
	var applyCalls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	migration := integrationTableMigration(revision, "create-revision-three-probe", "fairy_schema_revision_three_probe")
	originalApply := migration.Apply
	migration.Apply = func(ctx context.Context, connection *sql.Conn) error {
		if call := applyCalls.Add(1); call != 1 {
			return fmt.Errorf("revision 3 Apply called concurrently %d times", call)
		}
		close(entered)
		select {
		case <-release:
			return originalApply(ctx, connection)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	migrations := append(append([]Migration(nil), base...), migration)
	firstContext, cancelFirst := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- MigrateSchema(firstContext, database, migrations)
	}()

	select {
	case <-entered:
	case err := <-firstDone:
		t.Fatalf("first migration returned before holding lock: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("first migration did not reach its Apply callback")
	}

	secondContext, cancelSecond := context.WithTimeout(t.Context(), 5*time.Second)
	secondErr := MigrateSchema(secondContext, database, migrations)
	cancelSecond()
	close(release)
	firstErr := <-firstDone
	if !errors.Is(secondErr, ErrMigrationLockUnavailable) {
		t.Fatalf("concurrent MigrateSchema() error = %v, want ErrMigrationLockUnavailable", secondErr)
	}
	if firstErr != nil {
		t.Fatalf("lock-owning MigrateSchema() error = %v", firstErr)
	}
	if got := applyCalls.Load(); got != 1 {
		t.Fatalf("revision 3 Apply calls = %d, want 1", got)
	}
}
