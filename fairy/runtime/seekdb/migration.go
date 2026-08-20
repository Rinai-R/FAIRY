package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	schemaMigrationLockName    = "fairy_schema_migration_v1"
	schemaMigrationLockSeconds = 1
	schemaFailureWriteLimit    = 2 * time.Second
)

var (
	// ErrSchemaAbsent means the journal table has not been created yet.
	ErrSchemaAbsent = errors.New("SeekDB schema journal is absent")
	// ErrSchemaNotCurrent means the highest journal revision is not the expected committed revision.
	ErrSchemaNotCurrent = errors.New("SeekDB schema is not current")
	// ErrSchemaAhead means the database was migrated by a newer FAIRY runtime.
	ErrSchemaAhead = errors.New("SeekDB schema is newer than this runtime")
	// ErrSchemaChecksumMismatch prevents reusing a revision number for different DDL.
	ErrSchemaChecksumMismatch = errors.New("SeekDB schema migration checksum mismatch")
	// ErrSchemaJournalCorrupt means persisted journal state violates the migration contract.
	ErrSchemaJournalCorrupt = errors.New("SeekDB schema journal is corrupt")
	// ErrMigrationLockUnavailable means another session owns the bounded migration lock.
	ErrMigrationLockUnavailable = errors.New("SeekDB schema migration lock is unavailable")
)

// SchemaReadiness is the read-only schema state exposed to runtime readiness.
type SchemaReadiness string

const (
	SchemaAbsent     SchemaReadiness = "absent"
	SchemaNotCurrent SchemaReadiness = "not-current"
	SchemaCurrent    SchemaReadiness = "current"
)

// MigrationState is the durable state of one schema revision attempt.
type MigrationState string

const (
	MigrationApplying MigrationState = "applying"
	MigrationCurrent  MigrationState = "current"
	MigrationFailed   MigrationState = "failed"
)

// Revision identifies immutable migration content by a monotonic number and SHA-256 digest.
type Revision struct {
	Number   int64             `json:"number"`
	Checksum [sha256.Size]byte `json:"-"`
}

// MarshalJSON keeps the non-secret checksum readable in readiness diagnostics.
func (revision Revision) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Number      int64  `json:"number"`
		ChecksumHex string `json:"checksum"`
	}{
		Number:      revision.Number,
		ChecksumHex: hex.EncodeToString(revision.Checksum[:]),
	})
}

// Migration applies and then verifies one idempotent SeekDB schema revision.
// Apply must tolerate retry after partial DDL because SeekDB DDL implicitly commits.
type Migration struct {
	Revision Revision
	Name     string
	Apply    func(context.Context, *sql.Conn) error
	Verify   func(context.Context, *sql.Conn) error
}

// JournalEntry is the durable projection of one schema revision.
type JournalEntry struct {
	Revision     Revision       `json:"revision"`
	State        MigrationState `json:"state"`
	AttemptCount int64          `json:"attemptCount"`
	StartedAtMS  int64          `json:"startedAtMs"`
	FinishedAtMS *int64         `json:"finishedAtMs,omitempty"`
	ErrorCode    string         `json:"errorCode,omitempty"`
}

// SchemaStatus reports the expected and highest observed schema revisions.
type SchemaStatus struct {
	State    SchemaReadiness `json:"state"`
	Expected Revision        `json:"expected"`
	Observed *JournalEntry   `json:"observed,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

// MigrateSchema serially applies idempotent migrations under a session-scoped lock.
// It never pretends that SeekDB DDL is transactional: journal transitions are
// committed separately and a partial applying/failed revision is retried.
func MigrateSchema(ctx context.Context, database *sql.DB, migrations []Migration) (returnErr error) {
	if ctx == nil {
		return errors.New("SeekDB migration context is required")
	}
	if database == nil {
		return errors.New("SeekDB migration database is required")
	}
	if err := validateMigrations(migrations); err != nil {
		return err
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire SeekDB migration connection: %w", err)
	}
	defer connection.Close()
	if err := acquireMigrationLock(ctx, connection); err != nil {
		// GET_LOCK is session-scoped. If the query was interrupted after the
		// server acquired the lock, returning this physical connection to the
		// pool would retain an unobservable lock.
		discardSQLConnection(connection)
		return err
	}
	defer func() {
		if err := releaseMigrationLock(context.Background(), connection); err != nil {
			discardSQLConnection(connection)
			returnErr = errors.Join(returnErr, err)
		}
	}()

	if _, err := connection.ExecContext(ctx, createSchemaJournalSQL); err != nil {
		return fmt.Errorf("create SeekDB schema journal: %w", err)
	}
	latest, err := readLatestJournal(ctx, connection)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	lastExpected := migrations[len(migrations)-1].Revision.Number
	if err == nil && latest.Revision.Number > lastExpected {
		return fmt.Errorf("%w: observed revision %d, expected at most %d", ErrSchemaAhead, latest.Revision.Number, lastExpected)
	}

	for _, migration := range migrations {
		entry, entryErr := readJournalRevision(ctx, connection, migration.Revision.Number)
		if entryErr != nil && !errors.Is(entryErr, sql.ErrNoRows) {
			return entryErr
		}
		if entryErr == nil {
			if entry.Revision.Checksum != migration.Revision.Checksum {
				return fmt.Errorf("%w: revision %d", ErrSchemaChecksumMismatch, migration.Revision.Number)
			}
			if entry.State == MigrationCurrent {
				continue
			}
			if latest != nil && latest.Revision.Number > migration.Revision.Number {
				return fmt.Errorf("%w: partial revision %d exists below revision %d", ErrSchemaJournalCorrupt, migration.Revision.Number, latest.Revision.Number)
			}
			if entry.State != MigrationApplying && entry.State != MigrationFailed {
				return fmt.Errorf("%w: revision %d has state %q", ErrSchemaJournalCorrupt, migration.Revision.Number, entry.State)
			}
		} else if latest != nil && latest.Revision.Number > migration.Revision.Number {
			return fmt.Errorf("%w: revision %d is missing below revision %d", ErrSchemaJournalCorrupt, migration.Revision.Number, latest.Revision.Number)
		}

		if err := markMigrationApplying(ctx, connection, migration.Revision, entryErr == nil); err != nil {
			return err
		}
		if err := migration.Apply(ctx, connection); err != nil {
			markMigrationFailed(connection, migration.Revision, "APPLY_FAILED")
			return fmt.Errorf("apply SeekDB migration %d (%s): %w", migration.Revision.Number, migration.Name, err)
		}
		if err := migration.Verify(ctx, connection); err != nil {
			markMigrationFailed(connection, migration.Revision, "VERIFY_FAILED")
			return fmt.Errorf("verify SeekDB migration %d (%s): %w", migration.Revision.Number, migration.Name, err)
		}
		if err := markMigrationCurrent(ctx, connection, migration.Revision); err != nil {
			markMigrationFailed(connection, migration.Revision, "COMMIT_FAILED")
			return err
		}
		latest = &JournalEntry{Revision: migration.Revision, State: MigrationCurrent}
	}
	return nil
}

// CheckSchema performs a read-only check of the highest journal revision.
// It never creates, repairs, or enumerates application schema objects.
func CheckSchema(ctx context.Context, database *sql.DB, expected Revision) (SchemaStatus, error) {
	status := SchemaStatus{State: SchemaNotCurrent, Expected: expected}
	if ctx == nil {
		return status, errors.New("SeekDB schema readiness context is required")
	}
	if database == nil {
		return status, errors.New("SeekDB schema readiness database is required")
	}
	if err := validateRevision(expected); err != nil {
		return status, err
	}
	entry, err := readLatestJournal(ctx, database)
	if err != nil {
		switch {
		case isMySQLError(err, 1146):
			status.State = SchemaAbsent
			status.Reason = "journal-absent"
			return status, ErrSchemaAbsent
		case errors.Is(err, sql.ErrNoRows):
			status.Reason = "journal-empty"
			return status, ErrSchemaNotCurrent
		case isMySQLError(err, 1054), errors.Is(err, ErrSchemaJournalCorrupt):
			status.Reason = "journal-shape-invalid"
			return status, errors.Join(ErrSchemaNotCurrent, ErrSchemaJournalCorrupt)
		default:
			return status, fmt.Errorf("read SeekDB schema readiness: %w", err)
		}
	}
	status.Observed = entry
	if entry.Revision.Number > expected.Number {
		status.Reason = "schema-ahead"
		return status, errors.Join(ErrSchemaNotCurrent, fmt.Errorf("%w: observed revision %d, expected %d", ErrSchemaAhead, entry.Revision.Number, expected.Number))
	}
	if entry.Revision.Number != expected.Number {
		status.Reason = "revision-mismatch"
		return status, ErrSchemaNotCurrent
	}
	if entry.Revision.Checksum != expected.Checksum {
		status.Reason = "checksum-mismatch"
		return status, errors.Join(ErrSchemaNotCurrent, ErrSchemaChecksumMismatch)
	}
	if entry.State != MigrationCurrent {
		status.Reason = "migration-" + string(entry.State)
		return status, ErrSchemaNotCurrent
	}
	status.State = SchemaCurrent
	return status, nil
}

func validateMigrations(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("at least one SeekDB migration is required")
	}
	var previous int64
	for index, migration := range migrations {
		if err := validateRevision(migration.Revision); err != nil {
			return fmt.Errorf("validate SeekDB migration %d: %w", index, err)
		}
		if migration.Revision.Number != previous+1 {
			return errors.New("SeekDB migration revisions must form a continuous chain starting at one")
		}
		if migration.Name == "" || migration.Name != strings.TrimSpace(migration.Name) || strings.ContainsRune(migration.Name, 0) {
			return fmt.Errorf("SeekDB migration %d name is required and must be clean", migration.Revision.Number)
		}
		if migration.Apply == nil || migration.Verify == nil {
			return fmt.Errorf("SeekDB migration %d apply and verify functions are required", migration.Revision.Number)
		}
		previous = migration.Revision.Number
	}
	return nil
}

func validateRevision(revision Revision) error {
	if revision.Number <= 0 {
		return errors.New("SeekDB schema revision number must be greater than zero")
	}
	if revision.Checksum == ([sha256.Size]byte{}) {
		return errors.New("SeekDB schema revision checksum is required")
	}
	return nil
}

func acquireMigrationLock(ctx context.Context, connection *sql.Conn) error {
	var acquired sql.NullInt64
	if err := connection.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", schemaMigrationLockName, schemaMigrationLockSeconds).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire SeekDB schema migration lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return ErrMigrationLockUnavailable
	}
	return nil
}

func releaseMigrationLock(ctx context.Context, connection *sql.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, schemaFailureWriteLimit)
	defer cancel()
	var released sql.NullInt64
	if err := connection.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", schemaMigrationLockName).Scan(&released); err != nil {
		return fmt.Errorf("release SeekDB schema migration lock: %w", err)
	}
	if !released.Valid || released.Int64 != 1 {
		return errors.New("SeekDB schema migration lock was not released")
	}
	return nil
}

func discardSQLConnection(connection *sql.Conn) {
	_ = connection.Raw(func(any) error { return driver.ErrBadConn })
}

func markMigrationApplying(ctx context.Context, connection *sql.Conn, revision Revision, exists bool) error {
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SeekDB migration revision %d attempt: %w", revision.Number, err)
	}
	defer transaction.Rollback()
	now := time.Now().UnixMilli()
	var result sql.Result
	if !exists {
		result, err = transaction.ExecContext(ctx, `
INSERT INTO schema_revisions(revision, checksum, status, attempt_count, started_at_ms, finished_at_ms, error_code)
VALUES (?, ?, 'applying', 1, ?, NULL, NULL)`, revision.Number, revision.Checksum[:], now)
	} else {
		result, err = transaction.ExecContext(ctx, `
UPDATE schema_revisions
SET status = 'applying', attempt_count = attempt_count + 1, started_at_ms = ?, finished_at_ms = NULL, error_code = NULL
WHERE revision = ? AND checksum = ? AND status IN ('applying', 'failed')`, now, revision.Number, revision.Checksum[:])
	}
	if err != nil {
		return fmt.Errorf("start SeekDB migration revision %d attempt: %w", revision.Number, err)
	}
	if err := requireOneAffected(result, "start SeekDB migration revision", revision.Number); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit SeekDB migration revision %d attempt: %w", revision.Number, err)
	}
	return nil
}

func markMigrationCurrent(ctx context.Context, connection *sql.Conn, revision Revision) error {
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SeekDB migration revision commit: %w", err)
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `
UPDATE schema_revisions
SET status = 'current', finished_at_ms = ?, error_code = NULL
WHERE revision = ? AND checksum = ? AND status = 'applying'`, time.Now().UnixMilli(), revision.Number, revision.Checksum[:])
	if err != nil {
		return fmt.Errorf("commit SeekDB migration revision %d: %w", revision.Number, err)
	}
	if err := requireOneAffected(result, "commit SeekDB migration revision", revision.Number); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit SeekDB migration revision %d transaction: %w", revision.Number, err)
	}
	return nil
}

func markMigrationFailed(connection *sql.Conn, revision Revision, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaFailureWriteLimit)
	defer cancel()
	_, _ = connection.ExecContext(ctx, `
UPDATE schema_revisions
SET status = 'failed', finished_at_ms = ?, error_code = ?
WHERE revision = ? AND checksum = ? AND status = 'applying'`, time.Now().UnixMilli(), code, revision.Number, revision.Checksum[:])
}

func requireOneAffected(result sql.Result, operation string, revision int64) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s %d: read affected rows: %w", operation, revision, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s %d affected %d rows", ErrSchemaJournalCorrupt, operation, revision, affected)
	}
	return nil
}

func readLatestJournal(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (*JournalEntry, error) {
	return scanJournalRow(queryer.QueryRowContext(ctx, selectLatestSchemaJournalSQL))
}

func readJournalRevision(ctx context.Context, connection *sql.Conn, revision int64) (*JournalEntry, error) {
	return scanJournalRow(connection.QueryRowContext(ctx, selectSchemaJournalRevisionSQL, revision))
}

func scanJournalRow(row *sql.Row) (*JournalEntry, error) {
	var (
		entry         JournalEntry
		revisionRaw   any
		checksumRaw   any
		stateRaw      any
		attemptRaw    any
		startedAtRaw  any
		finishedAtRaw any
		errorCodeRaw  any
	)
	if err := row.Scan(&revisionRaw, &checksumRaw, &stateRaw, &attemptRaw, &startedAtRaw, &finishedAtRaw, &errorCodeRaw); err != nil {
		return nil, err
	}
	var err error
	if entry.Revision.Number, err = parseJournalInt("revision", revisionRaw); err != nil {
		return nil, err
	}
	if entry.AttemptCount, err = parseJournalInt("attempt_count", attemptRaw); err != nil {
		return nil, err
	}
	if entry.StartedAtMS, err = parseJournalInt("started_at_ms", startedAtRaw); err != nil {
		return nil, err
	}
	checksum, err := parseJournalBytes("checksum", checksumRaw)
	if err != nil {
		return nil, err
	}
	if len(checksum) != sha256.Size {
		return nil, fmt.Errorf("%w: revision %d checksum length is %d", ErrSchemaJournalCorrupt, entry.Revision.Number, len(checksum))
	}
	copy(entry.Revision.Checksum[:], checksum)
	if entry.Revision.Checksum == ([sha256.Size]byte{}) {
		return nil, fmt.Errorf("%w: revision %d checksum is empty", ErrSchemaJournalCorrupt, entry.Revision.Number)
	}
	state, err := parseJournalString("status", stateRaw)
	if err != nil {
		return nil, err
	}
	entry.State = MigrationState(state)
	if entry.Revision.Number <= 0 || entry.AttemptCount <= 0 || entry.StartedAtMS <= 0 {
		return nil, fmt.Errorf("%w: revision metadata is invalid", ErrSchemaJournalCorrupt)
	}
	if entry.State != MigrationApplying && entry.State != MigrationCurrent && entry.State != MigrationFailed {
		return nil, fmt.Errorf("%w: revision %d has state %q", ErrSchemaJournalCorrupt, entry.Revision.Number, entry.State)
	}
	if finishedAtRaw != nil {
		finishedAt, err := parseJournalInt("finished_at_ms", finishedAtRaw)
		if err != nil {
			return nil, err
		}
		entry.FinishedAtMS = &finishedAt
	}
	if errorCodeRaw != nil {
		entry.ErrorCode, err = parseJournalString("error_code", errorCodeRaw)
		if err != nil {
			return nil, err
		}
		if entry.ErrorCode == "" {
			return nil, fmt.Errorf("%w: revision %d error code must be NULL or non-empty", ErrSchemaJournalCorrupt, entry.Revision.Number)
		}
	}
	if entry.FinishedAtMS != nil && *entry.FinishedAtMS < entry.StartedAtMS {
		return nil, fmt.Errorf("%w: revision %d finishes before it starts", ErrSchemaJournalCorrupt, entry.Revision.Number)
	}
	if entry.ErrorCode != strings.TrimSpace(entry.ErrorCode) || strings.ContainsRune(entry.ErrorCode, 0) {
		return nil, fmt.Errorf("%w: revision %d error code is invalid", ErrSchemaJournalCorrupt, entry.Revision.Number)
	}
	if entry.State == MigrationApplying && (entry.FinishedAtMS != nil || entry.ErrorCode != "") {
		return nil, fmt.Errorf("%w: applying revision has terminal metadata", ErrSchemaJournalCorrupt)
	}
	if entry.State == MigrationCurrent && (entry.FinishedAtMS == nil || entry.ErrorCode != "") {
		return nil, fmt.Errorf("%w: current revision terminal metadata is invalid", ErrSchemaJournalCorrupt)
	}
	if entry.State == MigrationFailed && (entry.FinishedAtMS == nil || entry.ErrorCode == "") {
		return nil, fmt.Errorf("%w: failed revision terminal metadata is invalid", ErrSchemaJournalCorrupt)
	}
	return &entry, nil
}

func parseJournalInt(column string, raw any) (int64, error) {
	if raw == nil {
		return 0, fmt.Errorf("%w: %s is NULL or empty", ErrSchemaJournalCorrupt, column)
	}
	var text string
	switch value := raw.(type) {
	case int64:
		return value, nil
	case uint64:
		if value > uint64(^uint64(0)>>1) {
			return 0, fmt.Errorf("%w: %s overflows int64", ErrSchemaJournalCorrupt, column)
		}
		return int64(value), nil
	case []byte:
		text = string(value)
	case string:
		text = value
	default:
		return 0, fmt.Errorf("%w: %s has unsupported type", ErrSchemaJournalCorrupt, column)
	}
	if text == "" {
		return 0, fmt.Errorf("%w: %s is NULL or empty", ErrSchemaJournalCorrupt, column)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s is not an integer", ErrSchemaJournalCorrupt, column)
	}
	return value, nil
}

func parseJournalBytes(column string, raw any) ([]byte, error) {
	switch value := raw.(type) {
	case []byte:
		return append([]byte(nil), value...), nil
	case string:
		return []byte(value), nil
	case nil:
		return nil, fmt.Errorf("%w: %s is NULL", ErrSchemaJournalCorrupt, column)
	default:
		return nil, fmt.Errorf("%w: %s has unsupported type", ErrSchemaJournalCorrupt, column)
	}
}

func parseJournalString(column string, raw any) (string, error) {
	switch value := raw.(type) {
	case []byte:
		return string(value), nil
	case string:
		return value, nil
	case nil:
		return "", fmt.Errorf("%w: %s is NULL", ErrSchemaJournalCorrupt, column)
	default:
		return "", fmt.Errorf("%w: %s has unsupported type", ErrSchemaJournalCorrupt, column)
	}
}

func isMySQLError(err error, number uint16) bool {
	return IsErrno(err, number)
}

const createSchemaJournalSQL = `
CREATE TABLE IF NOT EXISTS schema_revisions (
  revision BIGINT NOT NULL,
  checksum BINARY(32) NOT NULL,
  status VARCHAR(16) NOT NULL,
  attempt_count BIGINT NOT NULL,
  started_at_ms BIGINT NOT NULL,
  finished_at_ms BIGINT NULL,
  error_code VARCHAR(64) NULL,
  PRIMARY KEY (revision)
)`

const selectLatestSchemaJournalSQL = `
SELECT revision, checksum, status, attempt_count, started_at_ms, finished_at_ms, error_code
FROM schema_revisions
ORDER BY revision DESC
LIMIT 1`

const selectSchemaJournalRevisionSQL = `
SELECT revision, checksum, status, attempt_count, started_at_ms, finished_at_ms, error_code
FROM schema_revisions
WHERE revision = ?`
