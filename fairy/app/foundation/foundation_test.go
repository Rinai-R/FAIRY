package foundation

import (
	"context"
	"database/sql"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/context/character"
	"fairy/runtime/config"
	"fairy/runtime/seekdb"
)

type fakeRuntime struct {
	database *sql.DB
	closeErr error

	mu         sync.Mutex
	closeCalls int
}

func (f *fakeRuntime) SQL() *sql.DB { return f.database }

func (*fakeRuntime) Descriptor() seekdb.Descriptor {
	return seekdb.Descriptor{BinaryName: "seekdb", Address: "127.0.0.1:2881", Database: "fairy"}
}

func (f *fakeRuntime) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return f.closeErr
}

func (f *fakeRuntime) closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls
}

func TestOpenFoundationOrdersReadinessAndIgnoresLegacyEnvironment(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "seekdb")
	characterRoot := filepath.Join(t.TempDir(), "characters")
	runtimeConfig := testRuntimeConfig(dataDir)
	localRuntime := &fakeRuntime{database: new(sql.DB)}
	var order []string
	var requested []string
	getenv := func(name string) string {
		requested = append(requested, name)
		switch name {
		case seekdb.EnvBinaryPath:
			return runtimeConfig.BinaryPath
		case seekdb.EnvDataDir:
			return runtimeConfig.DataDir
		case seekdb.EnvAddress:
			return runtimeConfig.Address
		case "FAIRY_DATABASE_URL", "FAIRY_DB_QUERY_TIMEOUT", "QDRANT_URL", "PGVECTOR_URL":
			return "invalid-legacy-sentinel"
		default:
			return ""
		}
	}
	operations := foundationOperations{
		configFromEnv: func(source func(string) string) (seekdb.Config, error) {
			order = append(order, "config")
			return seekdb.ConfigFromEnv(source)
		},
		openRuntime: func(context.Context, seekdb.Config) (runtimeBoundary, error) {
			order = append(order, "open")
			return localRuntime, nil
		},
		migrate: func(context.Context, *sql.DB, []seekdb.Migration) error {
			order = append(order, "migrate")
			return nil
		},
		checkSchema: func(_ context.Context, _ *sql.DB, expected seekdb.Revision) (seekdb.SchemaStatus, error) {
			order = append(order, "check")
			return seekdb.SchemaStatus{State: seekdb.SchemaCurrent, Expected: expected}, nil
		},
		openCipher: func(root string) (*config.SecretCipher, error) {
			order = append(order, "key")
			return config.SecretCipherFromDataDir(root)
		},
	}

	opened, err := open(t.Context(), Options{CharacterRoot: characterRoot, Getenv: getenv}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"config", "open", "migrate", "check", "key"}) {
		t.Fatalf("startup order = %v", order)
	}
	for _, name := range requested {
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "POSTGRES") || strings.Contains(upper, "PGVECTOR") || strings.Contains(upper, "QDRANT") || strings.HasPrefix(upper, "FAIRY_DB") || name == "FAIRY_DATABASE_URL" {
			t.Fatalf("foundation requested legacy environment variable %q", name)
		}
	}
	if opened.Documents == nil || opened.Secrets == nil || opened.Profile == nil || opened.Identity == nil || opened.Characters == nil {
		t.Fatalf("foundation stores are incomplete: %#v", opened)
	}
	if !slices.Equal(order, []string{"config", "open", "migrate", "check", "key"}) {
		t.Fatalf("startup order changed before dynamic status = %v", order)
	}
	status, err := opened.Status(t.Context())
	if err != nil || status.Storage != "seekdb" || status.Schema.State != seekdb.SchemaCurrent || !status.SecretsReady {
		t.Fatalf("Status() = (%#v, %v)", status, err)
	}
	if status.SeekDB.BinaryName != "seekdb" || strings.Contains(strings.ToLower(status.SeekDB.BinaryName), "tmp") {
		t.Fatalf("unsafe descriptor = %#v", status.SeekDB)
	}
	if _, err := opened.SQL(); err != nil {
		t.Fatalf("SQL() before close: %v", err)
	}
	if err := opened.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if localRuntime.closes() != 1 {
		t.Fatalf("runtime Close calls = %d, want 1", localRuntime.closes())
	}
	if _, err := opened.SQL(); !errors.Is(err, ErrFoundationClosed) {
		t.Fatalf("SQL() after close error = %v", err)
	}
	if _, err := opened.Status(t.Context()); !errors.Is(err, ErrFoundationClosed) {
		t.Fatalf("Status() after close error = %v", err)
	}
}

func TestOpenFoundationFailsClosedAndReleasesRuntime(t *testing.T) {
	errMigration := errors.New("migration failed")
	errReadiness := errors.New("readiness failed")
	errCipher := errors.New("master key failed")
	tests := []struct {
		name      string
		database  *sql.DB
		migrate   error
		check     error
		state     seekdb.SchemaReadiness
		cipher    error
		root      string
		want      error
		wantSteps []string
	}{
		{name: "missing SQL", state: seekdb.SchemaCurrent, want: ErrSeekDBUnavailable, wantSteps: []string{"open"}},
		{name: "migration", database: new(sql.DB), migrate: errMigration, state: seekdb.SchemaCurrent, want: errMigration, wantSteps: []string{"open", "migrate"}},
		{name: "readiness error", database: new(sql.DB), check: errReadiness, state: seekdb.SchemaNotCurrent, want: ErrSchemaNotReady, wantSteps: []string{"open", "migrate", "check"}},
		{name: "readiness state", database: new(sql.DB), state: seekdb.SchemaNotCurrent, want: ErrSchemaNotReady, wantSteps: []string{"open", "migrate", "check"}},
		{name: "master key", database: new(sql.DB), state: seekdb.SchemaCurrent, cipher: errCipher, want: errCipher, wantSteps: []string{"open", "migrate", "check", "key"}},
		{name: "character root", database: new(sql.DB), state: seekdb.SchemaCurrent, root: "relative", want: character.ErrCharacterVisualRootInvalid, wantSteps: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "seekdb")
			root := test.root
			if root == "" {
				root = filepath.Join(t.TempDir(), "characters")
			}
			localRuntime := &fakeRuntime{database: test.database}
			var steps []string
			operations := foundationOperations{
				configFromEnv: func(func(string) string) (seekdb.Config, error) { return testRuntimeConfig(dataDir), nil },
				openRuntime: func(context.Context, seekdb.Config) (runtimeBoundary, error) {
					steps = append(steps, "open")
					return localRuntime, nil
				},
				migrate: func(context.Context, *sql.DB, []seekdb.Migration) error {
					steps = append(steps, "migrate")
					return test.migrate
				},
				checkSchema: func(_ context.Context, _ *sql.DB, expected seekdb.Revision) (seekdb.SchemaStatus, error) {
					steps = append(steps, "check")
					return seekdb.SchemaStatus{State: test.state, Expected: expected}, test.check
				},
				openCipher: func(root string) (*config.SecretCipher, error) {
					steps = append(steps, "key")
					if test.cipher != nil {
						return nil, test.cipher
					}
					return config.SecretCipherFromDataDir(root)
				},
			}
			opened, err := open(t.Context(), Options{CharacterRoot: root}, operations)
			if opened != nil || !errors.Is(err, test.want) {
				t.Fatalf("open() = (%#v, %v), want nil and %v", opened, err, test.want)
			}
			if !slices.Equal(steps, test.wantSteps) {
				t.Fatalf("steps = %v, want %v", steps, test.wantSteps)
			}
			wantCloses := 1
			if len(test.wantSteps) == 0 {
				wantCloses = 0
			}
			if localRuntime.closes() != wantCloses {
				t.Fatalf("runtime Close calls = %d, want %d", localRuntime.closes(), wantCloses)
			}
		})
	}
}

func TestFoundationStatusRechecksSchemaAndFailsClosed(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "seekdb")
	localRuntime := &fakeRuntime{database: new(sql.DB)}
	checkCalls := 0
	operations := foundationOperations{
		configFromEnv: func(func(string) string) (seekdb.Config, error) {
			return testRuntimeConfig(dataDir), nil
		},
		openRuntime: func(context.Context, seekdb.Config) (runtimeBoundary, error) {
			return localRuntime, nil
		},
		migrate: func(context.Context, *sql.DB, []seekdb.Migration) error { return nil },
		checkSchema: func(_ context.Context, _ *sql.DB, expected seekdb.Revision) (seekdb.SchemaStatus, error) {
			checkCalls++
			if checkCalls == 1 {
				return seekdb.SchemaStatus{State: seekdb.SchemaCurrent, Expected: expected}, nil
			}
			return seekdb.SchemaStatus{State: seekdb.SchemaNotCurrent, Expected: expected}, seekdb.ErrSchemaChecksumMismatch
		},
		openCipher: config.SecretCipherFromDataDir,
	}

	opened, err := open(t.Context(), Options{CharacterRoot: filepath.Join(t.TempDir(), "characters")}, operations)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := opened.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()

	status, err := opened.Status(t.Context())
	if status != (Status{}) || !errors.Is(err, ErrSchemaNotReady) || !errors.Is(err, seekdb.ErrSchemaChecksumMismatch) {
		t.Fatalf("Status() = (%#v, %v), want fail-closed checksum mismatch", status, err)
	}
	if checkCalls != 2 {
		t.Fatalf("CheckSchema calls = %d, want startup plus dynamic readiness", checkCalls)
	}
}

func TestFoundationCloseIsBoundedAndReturnsRuntimeErrorOnce(t *testing.T) {
	closeFailure := errors.New("runtime close failed")
	localRuntime := &fakeRuntime{database: new(sql.DB), closeErr: closeFailure}
	operations := foundationOperations{
		configFromEnv: func(func(string) string) (seekdb.Config, error) {
			configured := testRuntimeConfig(filepath.Join(t.TempDir(), "seekdb"))
			configured.ShutdownLimit = 25 * time.Millisecond
			return configured, nil
		},
		openRuntime: func(context.Context, seekdb.Config) (runtimeBoundary, error) { return localRuntime, nil },
		migrate:     func(context.Context, *sql.DB, []seekdb.Migration) error { return nil },
		checkSchema: func(_ context.Context, _ *sql.DB, expected seekdb.Revision) (seekdb.SchemaStatus, error) {
			return seekdb.SchemaStatus{State: seekdb.SchemaCurrent, Expected: expected}, nil
		},
		openCipher: config.SecretCipherFromDataDir,
	}
	opened, err := open(t.Context(), Options{CharacterRoot: filepath.Join(t.TempDir(), "characters")}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(context.Background()); !errors.Is(err, closeFailure) {
		t.Fatalf("Close() error = %v, want runtime failure", err)
	}
	if err := opened.Close(t.Context()); !errors.Is(err, closeFailure) {
		t.Fatalf("second Close() error = %v, want same runtime failure", err)
	}
	if localRuntime.closes() != 1 {
		t.Fatalf("runtime Close calls = %d, want 1", localRuntime.closes())
	}
}

func TestFoundationValidatesBoundaryBeforeOpeningRuntime(t *testing.T) {
	if _, err := Open(nil, Options{CharacterRoot: t.TempDir()}); !errors.Is(err, ErrLifetimeContextRequired) {
		t.Fatalf("Open(nil) error = %v", err)
	}
	if _, err := open(t.Context(), Options{}, productionOperations); !errors.Is(err, ErrCharacterRootRequired) {
		t.Fatalf("open(missing root) error = %v", err)
	}
	if err := (*Foundation)(nil).Close(t.Context()); err != nil {
		t.Fatalf("nil Close() = %v", err)
	}
}

func TestOpenFoundationUsesProcessEnvironmentByDefault(t *testing.T) {
	t.Setenv("FAIRY_FOUNDATION_GETENV_TEST", "from-process")
	seen := ""
	operations := productionOperations
	operations.configFromEnv = func(getenv func(string) string) (seekdb.Config, error) {
		seen = getenv("FAIRY_FOUNDATION_GETENV_TEST")
		return seekdb.Config{}, errors.New("stop after environment boundary")
	}

	if opened, err := open(t.Context(), Options{CharacterRoot: t.TempDir()}, operations); opened != nil || err == nil {
		t.Fatalf("open() = (%#v, %v), want injected configuration failure", opened, err)
	}
	if seen != "from-process" {
		t.Fatalf("default getenv returned %q, want process environment", seen)
	}
}

func TestFoundationPackageDoesNotImportLegacyStorage(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	directory := filepath.Dir(current)
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range files {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			upper := strings.ToUpper(path)
			if strings.Contains(path, "runtime/database") || strings.Contains(path, "jackc/pgx") || strings.Contains(upper, "QDRANT") || strings.Contains(upper, "PGVECTOR") || strings.Contains(upper, "GORM") {
				t.Errorf("%s imports forbidden legacy storage %q", entry.Name(), path)
			}
		}
	}
}

func testRuntimeConfig(dataDir string) seekdb.Config {
	return seekdb.Config{
		BinaryPath: filepath.Join(string(filepath.Separator), "opt", "fairy", "seekdb"),
		DataDir:    dataDir, Address: "127.0.0.1:2881", Database: seekdb.DefaultDatabase, User: seekdb.DefaultUser,
		ConnectLimit: time.Second, StartLimit: time.Second, QueryLimit: time.Second,
		ShutdownLimit: time.Second, MaxOpenConns: 2, MaxIdleConns: 1,
	}
}
