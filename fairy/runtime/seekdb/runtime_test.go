package seekdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOpenAndCloseRuntime(t *testing.T) {
	config := testRuntimeConfig(t)
	options := testLaunchOptions()
	runtime, err := open(t.Context(), config, options)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Descriptor() != config.Descriptor() {
		t.Fatalf("Descriptor() = %+v", runtime.Descriptor())
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	select {
	case <-runtime.Done():
	case <-closeCtx.Done():
		t.Fatal("runtime did not close")
	}
}

func TestOpenBootstrapsConfiguredApplicationDatabase(t *testing.T) {
	config := testRuntimeConfig(t)
	var databases []string
	options := testLaunchOptions()
	options.database = func(_ Config, databaseName string) (*sql.DB, error) {
		databases = append(databases, databaseName)
		return sql.OpenDB(testSQLConnector{}), nil
	}
	runtime, err := open(t.Context(), config, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := runtime.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}()
	if !slices.Equal(databases, []string{seekDBBootstrapDatabase, config.Database}) {
		t.Fatalf("database sequence = %#v", databases)
	}
}

func TestOpenReportsApplicationDatabaseBootstrapFailure(t *testing.T) {
	config := testRuntimeConfig(t)
	options := testLaunchOptions()
	bootstrapFailure := errors.New("create application database denied")
	connections := 0
	options.database = func(_ Config, _ string) (*sql.DB, error) {
		connections++
		if connections == 2 {
			return nil, bootstrapFailure
		}
		return sql.OpenDB(testSQLConnector{}), nil
	}
	_, err := open(t.Context(), config, options)
	if !errors.Is(err, bootstrapFailure) && (err == nil || !strings.Contains(err.Error(), bootstrapFailure.Error())) {
		t.Fatalf("open() error = %v", err)
	}
	if connections != 2 {
		t.Fatalf("database connection attempts = %d", connections)
	}
}

func TestOpenRedactsPrivatePathsWhenEngineStartFails(t *testing.T) {
	config := testRuntimeConfig(t)
	private := config.DataDir
	options := testLaunchOptions()
	options.start = func(context.Context, Config, runtimePaths) (engineSession, error) {
		return nil, errors.New("engine refused " + private)
	}
	_, err := open(t.Context(), config, options)
	if err == nil || !strings.Contains(err.Error(), "start SeekDB engine") {
		t.Fatalf("open() error = %v", err)
	}
	if strings.Contains(err.Error(), private) {
		t.Fatalf("open() leaked private path: %v", err)
	}
}

func TestOpenStopsRuntimeOnStartupDeadline(t *testing.T) {
	config := testRuntimeConfig(t)
	config.StartLimit = 25 * time.Millisecond
	config.ShutdownLimit = time.Second
	options := testLaunchOptions()
	options.probe = func(context.Context, *sql.DB) error { return errors.New("not ready") }
	_, err := open(t.Context(), config, options)
	if !errors.Is(err, context.DeadlineExceeded) && (err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error())) {
		t.Fatalf("open() error = %v", err)
	}
}

func TestOpenStopsRuntimeWhenLifecycleContextCancels(t *testing.T) {
	config := testRuntimeConfig(t)
	ctx, cancel := context.WithCancel(t.Context())
	options := testLaunchOptions()
	options.probe = func(context.Context, *sql.DB) error {
		cancel()
		return errors.New("not ready")
	}
	_, err := open(ctx, config, options)
	if !errors.Is(err, context.Canceled) && (err == nil || !strings.Contains(err.Error(), context.Canceled.Error())) {
		t.Fatalf("open() error = %v", err)
	}
}

func TestOpenMissingLibraryFailsClosed(t *testing.T) {
	config := testRuntimeConfig(t)
	config.LibraryPath = filepath.Join(t.TempDir(), "missing-libseekdb.dylib")
	_, err := Open(t.Context(), config)
	if err == nil {
		t.Fatal("Open() succeeded without a SeekDB library")
	}
	if strings.Contains(err.Error(), "exec") || strings.Contains(err.Error(), "usr/bin/seekdb") {
		t.Fatalf("Open() fell back to a process: %v", err)
	}
}

func TestPrepareRuntimePathsRejectsWidePermissionsAndSymlinks(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows does not enforce POSIX directory modes")
	}
	wide := filepath.Join(t.TempDir(), "wide")
	if err := os.Mkdir(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRuntimePaths(wide); err == nil || !strings.Contains(err.Error(), "wider than 0700") {
		t.Fatalf("prepareRuntimePaths() error = %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRuntimePaths(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("prepareRuntimePaths() symlink error = %v", err)
	}
}

func TestPrepareRuntimePathsCreatesEmbeddedClientRunDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "seekdb-private")
	paths, err := prepareRuntimePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Run != filepath.Join(root, "run") {
		t.Fatalf("Run = %q", paths.Run)
	}
	info, err := os.Stat(paths.Run)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("run directory mode = %v", info.Mode())
	}
}

func testRuntimeConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		DataDir:       filepath.Join(t.TempDir(), "seekdb-private"),
		Database:      DefaultDatabase,
		ConnectLimit:  time.Second,
		StartLimit:    time.Second,
		QueryLimit:    10 * time.Millisecond,
		ShutdownLimit: time.Second,
		MaxOpenConns:  DefaultMaxOpenConns,
		MaxIdleConns:  DefaultMaxIdleConns,
	}
}

func testLaunchOptions() launchOptions {
	return launchOptions{
		start:             func(context.Context, Config, runtimePaths) (engineSession, error) { return nopEngine{}, nil },
		database:          func(Config, string) (*sql.DB, error) { return sql.OpenDB(testSQLConnector{}), nil },
		probe:             probeSQL,
		readinessInterval: 5 * time.Millisecond,
	}
}

type testSQLConnector struct{}

func (testSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return testSQLConnection{}, nil
}

func (testSQLConnector) Driver() driver.Driver { return testSQLDriver{} }

type testSQLDriver struct{}

func (testSQLDriver) Open(string) (driver.Conn, error) { return testSQLConnection{}, nil }

type testSQLConnection struct{}

func (testSQLConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not implemented by the test driver")
}

func (testSQLConnection) Close() error { return nil }

func (testSQLConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not implemented by the test driver")
}

func (testSQLConnection) Ping(context.Context) error { return nil }

func (testSQLConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
