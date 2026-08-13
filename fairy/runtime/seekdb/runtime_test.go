package seekdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const helperProcessEnvironment = "FAIRY_SEEKDB_HELPER_PROCESS"

func TestOpenAndCloseRuntime(t *testing.T) {
	config := testRuntimeConfig(t)
	options := testLaunchOptions(t, "block")
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
	options := testLaunchOptions(t, "block")
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
	options := testLaunchOptions(t, "block")
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

func TestOpenReportsEarlyProcessExitWithoutPrivatePaths(t *testing.T) {
	config := testRuntimeConfig(t)
	private := config.DataDir
	options := testLaunchOptions(t, "exit")
	options.probe = func(context.Context, *sql.DB) error { return errors.New("not ready") }
	_, err := open(t.Context(), config, options)
	if err == nil || !strings.Contains(err.Error(), "startup") {
		t.Fatalf("open() error = %v", err)
	}
	if strings.Contains(err.Error(), private) || strings.Contains(err.Error(), config.BinaryPath) {
		t.Fatalf("open() leaked private path: %v", err)
	}
}

func TestRuntimeReportsUnexpectedCleanExit(t *testing.T) {
	config := testRuntimeConfig(t)
	options := testLaunchOptions(t, "clean-exit")
	runtime, err := open(t.Context(), config, options)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not report the process exit")
	}
	if err := runtime.Err(); !errors.Is(err, ErrRuntimeExited) {
		t.Fatalf("Err() = %v, want ErrRuntimeExited", err)
	}
}

func TestOpenStopsRuntimeOnStartupDeadline(t *testing.T) {
	config := testRuntimeConfig(t)
	config.StartLimit = 25 * time.Millisecond
	config.ShutdownLimit = time.Second
	options := testLaunchOptions(t, "block")
	options.probe = func(context.Context, *sql.DB) error { return errors.New("not ready") }
	_, err := open(t.Context(), config, options)
	if !errors.Is(err, context.DeadlineExceeded) && (err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error())) {
		t.Fatalf("open() error = %v", err)
	}
}

func TestOpenStopsRuntimeWhenLifecycleContextCancels(t *testing.T) {
	config := testRuntimeConfig(t)
	ctx, cancel := context.WithCancel(t.Context())
	options := testLaunchOptions(t, "block")
	options.probe = func(context.Context, *sql.DB) error {
		cancel()
		return errors.New("not ready")
	}
	_, err := open(ctx, config, options)
	if !errors.Is(err, context.Canceled) && (err == nil || !strings.Contains(err.Error(), context.Canceled.Error())) {
		t.Fatalf("open() error = %v", err)
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

func TestSeekDBCommandUsesOnlyLocalNonSecretArguments(t *testing.T) {
	config := testRuntimeConfig(t)
	config.Address = "[::1]:3881"
	config.LibraryDirs = []string{filepath.Join(t.TempDir(), "compat")}
	paths := runtimePaths{
		Base: config.DataDir,
		Data: filepath.Join(config.DataDir, "store"),
		Redo: filepath.Join(config.DataDir, "redo"),
		Run:  filepath.Join(config.DataDir, "run"),
	}
	command := seekDBCommand(t.Context(), config, paths, io.Discard)
	want := []string{
		config.BinaryPath, "--nodaemon", "--embedded", "--port", "3881",
		"--base-dir", paths.Base, "--data-dir", paths.Data, "--redo-dir", paths.Redo,
		"--log-level", "WARN", "--use-ipv6",
	}
	if !slices.Equal(command.Args, want) {
		t.Fatalf("command args = %#v, want %#v", command.Args, want)
	}
	for _, argument := range command.Args[1:] {
		lower := strings.ToLower(argument)
		for _, forbidden := range []string{"--password", "--token", "--credential"} {
			if strings.HasPrefix(lower, forbidden) {
				t.Fatalf("command args contain credential flag %q", argument)
			}
		}
	}
	libraryEnvironment := false
	for _, variable := range command.Env {
		if strings.Contains(variable, config.LibraryDirs[0]) {
			libraryEnvironment = true
		}
	}
	if !libraryEnvironment {
		t.Fatalf("command environment does not contain the configured library directory")
	}
}

func TestBoundedOutputKeepsTail(t *testing.T) {
	output := newBoundedOutput(5)
	if _, err := output.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("defg")); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "cdefg" {
		t.Fatalf("String() = %q", got)
	}
}

func TestSeekDBHelperProcess(t *testing.T) {
	mode := os.Getenv(helperProcessEnvironment)
	if mode == "" {
		return
	}
	switch mode {
	case "exit":
		_, _ = os.Stderr.WriteString("seekdb failed under " + os.Getenv("FAIRY_SEEKDB_PRIVATE_PATH") + "\n")
		os.Exit(7)
	case "clean-exit":
		time.Sleep(25 * time.Millisecond)
		os.Exit(0)
	case "block":
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(9)
	}
}

func testRuntimeConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		BinaryPath:    os.Args[0],
		DataDir:       filepath.Join(t.TempDir(), "seekdb-private"),
		Address:       DefaultAddress,
		Database:      DefaultDatabase,
		User:          DefaultUser,
		ConnectLimit:  time.Second,
		StartLimit:    time.Second,
		QueryLimit:    10 * time.Millisecond,
		ShutdownLimit: time.Second,
		MaxOpenConns:  DefaultMaxOpenConns,
		MaxIdleConns:  DefaultMaxIdleConns,
	}
}

func testLaunchOptions(t *testing.T, mode string) launchOptions {
	t.Helper()
	return launchOptions{
		command: func(ctx context.Context, config Config, _ runtimePaths, output io.Writer) *exec.Cmd {
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSeekDBHelperProcess")
			command.Env = append(os.Environ(),
				helperProcessEnvironment+"="+mode,
				"FAIRY_SEEKDB_PRIVATE_PATH="+config.DataDir,
				"GORACE=atexit_sleep_ms=0",
			)
			command.Stdout = output
			command.Stderr = output
			return command
		},
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
