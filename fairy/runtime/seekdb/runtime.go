package seekdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultReadinessInterval = 50 * time.Millisecond
	maxProcessOutputBytes    = 64 << 10
)

var (
	ErrRuntimeClosed = errors.New("SeekDB runtime is closed")
	ErrRuntimeExited = errors.New("SeekDB runtime exited before readiness")
)

const seekDBBootstrapDatabase = "oceanbase"

type Runtime struct {
	config Config
	cmd    *exec.Cmd
	db     *sql.DB
	output *boundedOutput
	ctx    context.Context

	mu       sync.Mutex
	waitErr  error
	closing  bool
	wait     chan struct{}
	close    sync.Once
	closeErr error
}

type runtimePaths struct {
	Base string
	Data string
	Redo string
}

type launchOptions struct {
	command           func(context.Context, Config, runtimePaths, io.Writer) *exec.Cmd
	database          func(Config, string) (*sql.DB, error)
	probe             func(context.Context, *sql.DB) error
	readinessInterval time.Duration
}

func Open(ctx context.Context, config Config) (*Runtime, error) {
	return open(ctx, config, launchOptions{
		command:           seekDBCommand,
		database:          openSQLDatabaseForName,
		probe:             probeSQL,
		readinessInterval: defaultReadinessInterval,
	})
}

func open(ctx context.Context, config Config, options launchOptions) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("SeekDB lifecycle context is required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := validateExecutable(config.BinaryPath); err != nil {
		return nil, redactRuntimeError(config, err)
	}
	paths, err := prepareRuntimePaths(config.DataDir)
	if err != nil {
		return nil, redactRuntimeError(config, err)
	}
	if options.command == nil || options.database == nil || options.probe == nil {
		return nil, errors.New("SeekDB launch command, database and readiness probe are required")
	}
	if options.readinessInterval <= 0 {
		return nil, errors.New("SeekDB readiness interval must be greater than zero")
	}

	output := newBoundedOutput(maxProcessOutputBytes)
	bootstrapDatabase := config.Database
	if config.Database != seekDBBootstrapDatabase {
		bootstrapDatabase = seekDBBootstrapDatabase
	}
	database, err := options.database(config, bootstrapDatabase)
	if err != nil {
		return nil, redactRuntimeError(config, fmt.Errorf("configure SeekDB SQL connection: %w", err))
	}
	cmd := options.command(ctx, config, paths, output)
	if cmd == nil {
		_ = database.Close()
		return nil, errors.New("SeekDB launch command is nil")
	}
	runtime := &Runtime{config: config, cmd: cmd, db: database, output: output, ctx: ctx, wait: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		_ = database.Close()
		return nil, redactRuntimeError(config, fmt.Errorf("start SeekDB %s: %w", descriptorText(config), err))
	}
	go runtime.collectWait()

	readyCtx, cancel := context.WithTimeout(ctx, config.StartLimit)
	defer cancel()
	ticker := time.NewTicker(options.readinessInterval)
	defer ticker.Stop()
	for {
		probeCtx, probeCancel := context.WithTimeout(readyCtx, min(config.QueryLimit, options.readinessInterval))
		probeErr := options.probe(probeCtx, database)
		probeCancel()
		if probeErr == nil {
			if config.Database == bootstrapDatabase {
				return runtime, nil
			}
			if err := runtime.createAndSelectDatabase(readyCtx, options); err != nil {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), config.ShutdownLimit)
				closeErr := runtime.Close(closeCtx)
				closeCancel()
				return nil, redactRuntimeError(config, errors.Join(fmt.Errorf("initialize SeekDB application database: %w", err), closeErr))
			}
			return runtime, nil
		}
		select {
		case <-runtime.wait:
			if readyCtx.Err() != nil {
				return nil, redactRuntimeError(config, fmt.Errorf("wait for SeekDB %s readiness: %w", descriptorText(config), readyCtx.Err()))
			}
			startupErr := runtime.startupExitError()
			_ = database.Close()
			return nil, startupErr
		case <-readyCtx.Done():
			closeCtx, closeCancel := context.WithTimeout(context.Background(), config.ShutdownLimit)
			_ = runtime.Close(closeCtx)
			closeCancel()
			return nil, redactRuntimeError(config, fmt.Errorf("wait for SeekDB %s readiness: %w", descriptorText(config), readyCtx.Err()))
		case <-ticker.C:
		}
	}
}

func (r *Runtime) createAndSelectDatabase(ctx context.Context, options launchOptions) error {
	queryCtx, cancel := context.WithTimeout(ctx, r.config.QueryLimit)
	_, err := r.db.ExecContext(queryCtx, "CREATE DATABASE IF NOT EXISTS `"+r.config.Database+"`")
	cancel()
	if err != nil {
		return err
	}
	database, err := options.database(r.config, r.config.Database)
	if err != nil {
		return err
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, r.config.QueryLimit)
	err = options.probe(probeCtx, database)
	probeCancel()
	if err != nil {
		_ = database.Close()
		return err
	}
	old := r.db
	if err := old.Close(); err != nil {
		_ = database.Close()
		return err
	}
	r.db = database
	return nil
}

func (r *Runtime) Done() <-chan struct{} {
	if r == nil || r.wait == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return r.wait
}

func (r *Runtime) Err() error {
	if r == nil {
		return ErrRuntimeClosed
	}
	select {
	case <-r.wait:
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closing {
			return nil
		}
		return r.waitErr
	default:
		return nil
	}
}

func (r *Runtime) Descriptor() Descriptor {
	if r == nil {
		return Descriptor{}
	}
	return r.config.Descriptor()
}

func (r *Runtime) SQL() *sql.DB {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil
	}
	return r.db
}

func (r *Runtime) QueryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if r == nil || r.config.QueryLimit <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, r.config.QueryLimit)
}

func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("SeekDB close context is required")
	}
	r.close.Do(func() {
		r.closeErr = r.closeProcess(ctx)
	})
	return r.closeErr
}

func (r *Runtime) closeProcess(ctx context.Context) error {
	r.mu.Lock()
	r.closing = true
	process := r.cmd.Process
	database := r.db
	r.db = nil
	r.mu.Unlock()
	var databaseErr error
	if database != nil {
		databaseErr = database.Close()
	}
	if process == nil {
		return databaseErr
	}
	select {
	case <-r.wait:
		return databaseErr
	default:
	}
	if err := interruptProcess(process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.Join(databaseErr, redactRuntimeError(r.config, fmt.Errorf("interrupt SeekDB %s: %w", descriptorText(r.config), err)))
	}
	select {
	case <-r.wait:
		return databaseErr
	case <-ctx.Done():
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return errors.Join(databaseErr, redactRuntimeError(r.config, fmt.Errorf("kill SeekDB %s after shutdown deadline: %w", descriptorText(r.config), err)))
		}
		<-r.wait
		return errors.Join(databaseErr, redactRuntimeError(r.config, fmt.Errorf("SeekDB %s exceeded shutdown deadline: %w", descriptorText(r.config), ctx.Err())))
	}
}

func (r *Runtime) collectWait() {
	err := r.cmd.Wait()
	r.mu.Lock()
	if r.ctx.Err() != nil {
		err = r.ctx.Err()
	}
	r.waitErr = err
	r.mu.Unlock()
	close(r.wait)
}

func (r *Runtime) startupExitError() error {
	r.mu.Lock()
	err := r.waitErr
	r.mu.Unlock()
	detail := redactProcessOutput(r.config, r.output.String())
	if err == nil {
		err = ErrRuntimeExited
	}
	if detail == "" {
		return redactRuntimeError(r.config, fmt.Errorf("SeekDB %s startup: %w", descriptorText(r.config), err))
	}
	return redactRuntimeError(r.config, fmt.Errorf("SeekDB %s startup: %w: %s", descriptorText(r.config), err, detail))
}

func seekDBCommand(ctx context.Context, config Config, paths runtimePaths, output io.Writer) *exec.Cmd {
	_, rawPort, _ := net.SplitHostPort(config.Address)
	args := []string{
		"--nodaemon",
		"--embedded",
		"--port", rawPort,
		"--base-dir", paths.Base,
		"--data-dir", paths.Data,
		"--redo-dir", paths.Redo,
		"--log-level", "WARN",
	}
	host, _, _ := net.SplitHostPort(config.Address)
	if net.ParseIP(host).To4() == nil {
		args = append(args, "--use-ipv6")
	}
	cmd := exec.CommandContext(ctx, config.BinaryPath, args...)
	cmd.Dir = paths.Base
	cmd.Stdout = output
	cmd.Stderr = output
	if len(config.LibraryDirs) > 0 {
		cmd.Env = withDynamicLibraryPath(os.Environ(), config.LibraryDirs)
	}
	return cmd
}

func withDynamicLibraryPath(environment, directories []string) []string {
	name := "LD_LIBRARY_PATH"
	if goruntime.GOOS == "darwin" {
		name = "DYLD_FALLBACK_LIBRARY_PATH"
	} else if goruntime.GOOS == "windows" {
		name = "PATH"
	}
	prefix := name + "="
	cleaned := make([]string, 0, len(environment)+1)
	existing := ""
	for _, variable := range environment {
		if strings.HasPrefix(variable, prefix) {
			existing = strings.TrimPrefix(variable, prefix)
			continue
		}
		cleaned = append(cleaned, variable)
	}
	search := append([]string(nil), directories...)
	if existing != "" {
		search = append(search, existing)
	}
	return append(cleaned, prefix+strings.Join(search, string(os.PathListSeparator)))
}

func validateExecutable(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve SeekDB binary: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("inspect SeekDB binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("SeekDB binary must be a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("SeekDB binary is not executable")
	}
	return nil
}

func prepareRuntimePaths(root string) (runtimePaths, error) {
	if err := ensurePrivateDirectory(root); err != nil {
		return runtimePaths{}, fmt.Errorf("prepare SeekDB data directory: %w", err)
	}
	paths := runtimePaths{Base: root, Data: filepath.Join(root, "store"), Redo: filepath.Join(root, "redo")}
	for _, path := range []string{paths.Data, paths.Redo} {
		if err := ensurePrivateDirectory(path); err != nil {
			return runtimePaths{}, fmt.Errorf("prepare SeekDB runtime directory: %w", err)
		}
	}
	return paths, nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("path must be a real directory, not a symlink or file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("directory permissions %04o are wider than 0700", info.Mode().Perm())
	}
	return nil
}

func descriptorText(config Config) string {
	descriptor := config.Descriptor()
	return descriptor.BinaryName + "@" + descriptor.Address + "/" + descriptor.Database
}

func redactRuntimeError(config Config, err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	message = strings.ReplaceAll(message, config.BinaryPath, "[seekdb-binary]")
	message = strings.ReplaceAll(message, config.DataDir, "[seekdb-data]")
	for _, directory := range config.LibraryDirs {
		message = strings.ReplaceAll(message, directory, "[seekdb-library]")
	}
	if config.Password != "" {
		message = strings.ReplaceAll(message, config.Password, "[seekdb-credential]")
	}
	return errors.New(message)
}

func redactProcessOutput(config Config, output string) string {
	output = strings.TrimSpace(output)
	output = strings.ReplaceAll(output, config.BinaryPath, "[seekdb-binary]")
	output = strings.ReplaceAll(output, config.DataDir, "[seekdb-data]")
	for _, directory := range config.LibraryDirs {
		output = strings.ReplaceAll(output, directory, "[seekdb-library]")
	}
	if config.Password != "" {
		output = strings.ReplaceAll(output, config.Password, "[seekdb-credential]")
	}
	return output
}

type boundedOutput struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newBoundedOutput(limit int) *boundedOutput {
	return &boundedOutput{limit: limit}
}

func (w *boundedOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(value) >= w.limit {
		w.data = append(w.data[:0], value[len(value)-w.limit:]...)
		return len(value), nil
	}
	overflow := len(w.data) + len(value) - w.limit
	if overflow > 0 {
		copy(w.data, w.data[overflow:])
		w.data = w.data[:len(w.data)-overflow]
	}
	w.data = append(w.data, value...)
	return len(value), nil
}

func (w *boundedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(append([]byte(nil), w.data...))
}
