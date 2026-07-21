package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	CurrentSchemaVersion = 2

	migrationLockKey int64 = 0x46414952595f4442
)

var (
	ErrSchemaAbsent     = errors.New("postgres schema is absent")
	ErrSchemaNotCurrent = errors.New("postgres schema is not current")
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

type SchemaStatus struct {
	ExpectedVersion int  `json:"expectedVersion"`
	AppliedVersion  int  `json:"appliedVersion"`
	Current         bool `json:"current"`
}

func Migrate(ctx context.Context, pool *Pool) error {
	migrations, err := LoadMigrations()
	if err != nil {
		return err
	}
	return migrate(ctx, rawPool(pool), migrations)
}

func VerifySchema(ctx context.Context, pool *Pool, expectedVersion int) (SchemaStatus, error) {
	migrations, err := LoadMigrations()
	if err != nil {
		return SchemaStatus{}, err
	}
	if expectedVersion <= 0 {
		expectedVersion = CurrentSchemaVersion
	}
	if expectedVersion > len(migrations) {
		return SchemaStatus{}, fmt.Errorf("expected schema version %d has no embedded migration", expectedVersion)
	}
	return verifySchema(ctx, rawPool(pool), migrations[:expectedVersion], expectedVersion)
}

func LoadMigrations() ([]Migration, error) {
	return loadMigrations(embeddedMigrations)
}

func rawPool(pool *Pool) *pgxpool.Pool {
	if pool == nil {
		return nil
	}
	return pool.Raw()
}

func migrate(ctx context.Context, pool *pgxpool.Pool, migrations []Migration) error {
	if pool == nil {
		return errors.New("database pool is not open")
	}
	if err := validateMigrations(migrations); err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("beginning migration transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("acquiring migration advisory lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS fairy_schema_migrations (
  version integer PRIMARY KEY CHECK (version > 0),
  name text NOT NULL,
  checksum_sha256 text NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("creating schema migrations table: %w", err)
	}
	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	byVersion := make(map[int]Migration, len(migrations))
	for _, migration := range migrations {
		byVersion[migration.Version] = migration
	}
	for version, row := range applied {
		migration, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("database schema version %d is newer than embedded migrations", version)
		}
		if row.Checksum != migration.Checksum {
			return fmt.Errorf("schema migration %04d checksum mismatch: database=%s embedded=%s", version, row.Checksum, migration.Checksum)
		}
	}
	for _, migration := range migrations {
		if _, ok := applied[migration.Version]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, migration.SQL); err != nil {
			return fmt.Errorf("applying schema migration %04d_%s: %w", migration.Version, migration.Name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO fairy_schema_migrations(version, name, checksum_sha256) VALUES ($1, $2, $3)", migration.Version, migration.Name, migration.Checksum); err != nil {
			return fmt.Errorf("recording schema migration %04d_%s: %w", migration.Version, migration.Name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing schema migration transaction: %w", err)
	}
	return nil
}

func verifySchema(ctx context.Context, pool *pgxpool.Pool, migrations []Migration, expectedVersion int) (SchemaStatus, error) {
	if pool == nil {
		return SchemaStatus{}, errors.New("database pool is not open")
	}
	if err := validateMigrations(migrations); err != nil {
		return SchemaStatus{}, err
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('fairy_schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return SchemaStatus{}, fmt.Errorf("checking schema migration table: %w", err)
	}
	if !exists {
		return SchemaStatus{ExpectedVersion: expectedVersion}, ErrSchemaAbsent
	}
	applied := make(map[int]migrationRow)
	rows, err := pool.Query(ctx, "SELECT version, name, checksum_sha256 FROM fairy_schema_migrations ORDER BY version ASC")
	if err != nil {
		return SchemaStatus{}, fmt.Errorf("reading schema migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row migrationRow
		if err := rows.Scan(&row.Version, &row.Name, &row.Checksum); err != nil {
			return SchemaStatus{}, fmt.Errorf("scanning schema migration: %w", err)
		}
		applied[row.Version] = row
	}
	if err := rows.Err(); err != nil {
		return SchemaStatus{}, fmt.Errorf("iterating schema migrations: %w", err)
	}
	status := SchemaStatus{ExpectedVersion: expectedVersion}
	for version := range applied {
		if version > status.AppliedVersion {
			status.AppliedVersion = version
		}
	}
	for _, migration := range migrations {
		row, ok := applied[migration.Version]
		if !ok {
			return status, fmt.Errorf("%w: expected version %d, applied version %d", ErrSchemaNotCurrent, expectedVersion, status.AppliedVersion)
		}
		if row.Checksum != migration.Checksum {
			return status, fmt.Errorf("schema migration %04d checksum mismatch: database=%s embedded=%s", migration.Version, row.Checksum, migration.Checksum)
		}
	}
	if status.AppliedVersion != expectedVersion {
		return status, fmt.Errorf("%w: expected version %d, applied version %d", ErrSchemaNotCurrent, expectedVersion, status.AppliedVersion)
	}
	status.Current = true
	return status, nil
}

type migrationRow struct {
	Version  int
	Name     string
	Checksum string
}

func readAppliedMigrations(ctx context.Context, tx pgx.Tx) (map[int]migrationRow, error) {
	rows, err := tx.Query(ctx, "SELECT version, name, checksum_sha256 FROM fairy_schema_migrations ORDER BY version ASC")
	if err != nil {
		return nil, fmt.Errorf("reading applied migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]migrationRow)
	for rows.Next() {
		var row migrationRow
		if err := rows.Scan(&row.Version, &row.Name, &row.Checksum); err != nil {
			return nil, fmt.Errorf("scanning applied migration: %w", err)
		}
		applied[row.Version] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating applied migrations: %w", err)
	}
	return applied, nil
}

func loadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations: %w", err)
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(fsys, path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(body)
		migrations = append(migrations, Migration{Version: version, Name: name, SQL: string(body), Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

func parseMigrationName(fileName string) (int, string, error) {
	base := strings.TrimSuffix(fileName, ".sql")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 || len(parts[0]) != 4 || parts[1] == "" {
		return 0, "", fmt.Errorf("migration file %s must be named NNNN_name.sql", fileName)
	}
	version, err := strconv.Atoi(parts[0])
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("migration file %s has invalid version", fileName)
	}
	return version, parts[1], nil
}

func validateMigrations(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("no embedded PostgreSQL migrations")
	}
	for i, migration := range migrations {
		wantVersion := i + 1
		if migration.Version != wantVersion {
			return fmt.Errorf("migration versions must be contiguous: got %d, want %d", migration.Version, wantVersion)
		}
		if migration.Name == "" || strings.TrimSpace(migration.Name) != migration.Name {
			return fmt.Errorf("migration %04d has invalid name", migration.Version)
		}
		if strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf("migration %04d_%s is empty", migration.Version, migration.Name)
		}
		if len(migration.Checksum) != 64 {
			return fmt.Errorf("migration %04d_%s has invalid checksum", migration.Version, migration.Name)
		}
	}
	return nil
}

func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
