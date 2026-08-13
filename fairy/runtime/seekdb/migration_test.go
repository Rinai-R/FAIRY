package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestValidateMigrations(t *testing.T) {
	valid := func(number int64, name string) Migration {
		return Migration{
			Revision: testRevision(number, name),
			Name:     name,
			Apply:    func(context.Context, *sql.Conn) error { return nil },
			Verify:   func(context.Context, *sql.Conn) error { return nil },
		}
	}

	tests := []struct {
		name       string
		migrations func() []Migration
		wantError  string
	}{
		{
			name:       "valid ordered migrations",
			migrations: func() []Migration { return []Migration{valid(1, "create-core"), valid(2, "add-index")} },
		},
		{
			name:       "empty list",
			migrations: func() []Migration { return nil },
			wantError:  "at least one SeekDB migration is required",
		},
		{
			name: "non-positive revision",
			migrations: func() []Migration {
				migration := valid(1, "create-core")
				migration.Revision.Number = 0
				return []Migration{migration}
			},
			wantError: "revision number must be greater than zero",
		},
		{
			name: "missing checksum",
			migrations: func() []Migration {
				migration := valid(1, "create-core")
				migration.Revision.Checksum = [sha256.Size]byte{}
				return []Migration{migration}
			},
			wantError: "revision checksum is required",
		},
		{
			name:       "duplicate revision",
			migrations: func() []Migration { return []Migration{valid(1, "create-core"), valid(1, "duplicate")} },
			wantError:  "continuous chain starting at one",
		},
		{
			name:       "descending revisions",
			migrations: func() []Migration { return []Migration{valid(2, "add-index"), valid(1, "create-core")} },
			wantError:  "continuous chain starting at one",
		},
		{
			name:       "fresh database cannot start at revision two",
			migrations: func() []Migration { return []Migration{valid(2, "add-index")} },
			wantError:  "continuous chain starting at one",
		},
		{
			name:       "revision gap",
			migrations: func() []Migration { return []Migration{valid(1, "create-core"), valid(3, "add-index")} },
			wantError:  "continuous chain starting at one",
		},
		{
			name: "blank name",
			migrations: func() []Migration {
				migration := valid(1, "create-core")
				migration.Name = ""
				return []Migration{migration}
			},
			wantError: "name is required and must be clean",
		},
		{
			name: "name with surrounding whitespace",
			migrations: func() []Migration {
				migration := valid(1, "create-core")
				migration.Name = " create-core "
				return []Migration{migration}
			},
			wantError: "name is required and must be clean",
		},
		{
			name: "name with nul",
			migrations: func() []Migration {
				migration := valid(1, "create-core")
				migration.Name = "create\x00core"
				return []Migration{migration}
			},
			wantError: "name is required and must be clean",
		},
		{
			name: "missing apply",
			migrations: func() []Migration {
				migration := valid(1, "create-core")
				migration.Apply = nil
				return []Migration{migration}
			},
			wantError: "apply and verify functions are required",
		},
		{
			name: "missing verify",
			migrations: func() []Migration {
				migration := valid(1, "create-core")
				migration.Verify = nil
				return []Migration{migration}
			},
			wantError: "apply and verify functions are required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateMigrations(test.migrations())
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("validateMigrations() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("validateMigrations() error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestMigrateSchemaValidatesBeforeOpeningDatabase(t *testing.T) {
	state := &readOnlyDriverState{}
	database := sql.OpenDB(readOnlyConnector{state: state})
	defer database.Close()

	err := MigrateSchema(t.Context(), database, nil)
	if err == nil || !strings.Contains(err.Error(), "at least one SeekDB migration is required") {
		t.Fatalf("MigrateSchema() error = %v", err)
	}
	if got := state.connects.Load(); got != 0 {
		t.Fatalf("database connections = %d, want 0", got)
	}
}

func TestMigrationEntryPointsRejectMissingArguments(t *testing.T) {
	validRevision := testRevision(1, "create-core")
	validMigration := Migration{
		Revision: validRevision,
		Name:     "create-core",
		Apply:    func(context.Context, *sql.Conn) error { return nil },
		Verify:   func(context.Context, *sql.Conn) error { return nil },
	}

	tests := []struct {
		name      string
		operation func() error
		wantError string
	}{
		{
			name: "migration nil context",
			operation: func() error {
				return MigrateSchema(nil, new(sql.DB), []Migration{validMigration})
			},
			wantError: "migration context is required",
		},
		{
			name: "migration nil database",
			operation: func() error {
				return MigrateSchema(t.Context(), nil, []Migration{validMigration})
			},
			wantError: "migration database is required",
		},
		{
			name: "readiness nil context",
			operation: func() error {
				_, err := CheckSchema(nil, new(sql.DB), validRevision)
				return err
			},
			wantError: "readiness context is required",
		},
		{
			name: "readiness nil database",
			operation: func() error {
				_, err := CheckSchema(t.Context(), nil, validRevision)
				return err
			},
			wantError: "readiness database is required",
		},
		{
			name: "readiness invalid revision",
			operation: func() error {
				_, err := CheckSchema(t.Context(), new(sql.DB), Revision{})
				return err
			},
			wantError: "revision number must be greater than zero",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.operation()
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("operation error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestCheckSchemaUsesOneReadOnlyQuery(t *testing.T) {
	revision := testRevision(7, "current-schema")
	state := &readOnlyDriverState{
		values: []driver.Value{
			revision.Number,
			revision.Checksum[:],
			string(MigrationCurrent),
			int64(3),
			int64(1000),
			int64(2000),
			nil,
		},
	}
	database := sql.OpenDB(readOnlyConnector{state: state})
	defer database.Close()

	status, err := CheckSchema(t.Context(), database, revision)
	if err != nil {
		t.Fatalf("CheckSchema() error = %v", err)
	}
	if status.State != SchemaCurrent {
		t.Fatalf("status state = %q, want %q", status.State, SchemaCurrent)
	}
	if status.Observed == nil || status.Observed.Revision != revision {
		t.Fatalf("observed revision = %#v, want %#v", status.Observed, revision)
	}
	if got := state.queries.Load(); got != 1 {
		t.Fatalf("query count = %d, want 1", got)
	}
	if got := state.executes.Load(); got != 0 {
		t.Fatalf("exec count = %d, want 0", got)
	}
	if got := state.begins.Load(); got != 0 {
		t.Fatalf("transaction count = %d, want 0", got)
	}
	if got, want := normalizeSQL(state.lastQuery()), normalizeSQL(selectLatestSchemaJournalSQL); got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

func TestCheckSchemaClassifiesMalformedJournalAsNotCurrent(t *testing.T) {
	revision := testRevision(7, "current-schema")
	state := &readOnlyDriverState{
		values: []driver.Value{
			"not-an-integer",
			revision.Checksum[:],
			string(MigrationCurrent),
			int64(3),
			int64(1000),
			int64(2000),
			nil,
		},
	}
	database := sql.OpenDB(readOnlyConnector{state: state})
	defer database.Close()

	status, err := CheckSchema(t.Context(), database, revision)
	if !errors.Is(err, ErrSchemaNotCurrent) || !errors.Is(err, ErrSchemaJournalCorrupt) {
		t.Fatalf("CheckSchema() error = %v, want not-current and corrupt", err)
	}
	if status.State != SchemaNotCurrent || status.Reason != "journal-shape-invalid" {
		t.Fatalf("CheckSchema() status = %#v", status)
	}
}

func TestRevisionJSONIncludesChecksum(t *testing.T) {
	revision := testRevision(7, "current-schema")
	encoded, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); !strings.Contains(got, `"checksum":"`) || strings.Contains(got, `[`) {
		t.Fatalf("revision JSON = %s", got)
	}
}

func testRevision(number int64, source string) Revision {
	return Revision{Number: number, Checksum: sha256.Sum256([]byte(source))}
}

func normalizeSQL(statement string) string {
	return strings.Join(strings.Fields(statement), " ")
}

type readOnlyDriverState struct {
	connects atomic.Int64
	queries  atomic.Int64
	executes atomic.Int64
	begins   atomic.Int64

	mu       sync.Mutex
	query    string
	values   []driver.Value
	queryErr error
}

func (state *readOnlyDriverState) setLastQuery(query string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.query = query
}

func (state *readOnlyDriverState) lastQuery() string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.query
}

type readOnlyConnector struct {
	state *readOnlyDriverState
}

func (connector readOnlyConnector) Connect(context.Context) (driver.Conn, error) {
	connector.state.connects.Add(1)
	return &readOnlyConnection{state: connector.state}, nil
}

func (connector readOnlyConnector) Driver() driver.Driver {
	return readOnlyDriver{}
}

type readOnlyDriver struct{}

func (readOnlyDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("read-only test driver must be opened through its connector")
}

type readOnlyConnection struct {
	state *readOnlyDriverState
}

func (*readOnlyConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported by the read-only test driver")
}

func (*readOnlyConnection) Close() error { return nil }

func (connection *readOnlyConnection) Begin() (driver.Tx, error) {
	connection.state.begins.Add(1)
	return nil, errors.New("transactions are not allowed by the read-only test driver")
}

func (connection *readOnlyConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	connection.state.queries.Add(1)
	connection.state.setLastQuery(query)
	if connection.state.queryErr != nil {
		return nil, connection.state.queryErr
	}
	values := append([]driver.Value(nil), connection.state.values...)
	return &singleSchemaRow{values: values}, nil
}

func (connection *readOnlyConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	connection.state.executes.Add(1)
	return nil, errors.New("writes are not allowed by the read-only test driver")
}

type singleSchemaRow struct {
	values []driver.Value
	read   bool
}

func (*singleSchemaRow) Columns() []string {
	return []string{"revision", "checksum", "status", "attempt_count", "started_at_ms", "finished_at_ms", "error_code"}
}

func (*singleSchemaRow) Close() error { return nil }

func (row *singleSchemaRow) Next(destination []driver.Value) error {
	if row.read {
		return io.EOF
	}
	row.read = true
	copy(destination, row.values)
	return nil
}
