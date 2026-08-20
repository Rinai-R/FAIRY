package seekdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultReadinessInterval = 50 * time.Millisecond

var ErrRuntimeClosed = errors.New("SeekDB runtime is closed")

const seekDBBootstrapDatabase = "oceanbase"

type Runtime struct {
	config      Config
	engine      engineSession
	db          *sql.DB
	ctx         context.Context
	clientLease io.Closer

	mu         sync.Mutex
	waitErr    error
	closing    bool
	wait       chan struct{}
	leaseClose sync.Once
	leaseErr   error
	close      sync.Once
	closeErr   error
}

type runtimePaths struct {
	Base string
	Data string
	Redo string
	Run  string
}

type engineSession interface {
	Close(context.Context) error
}

type launchOptions struct {
	start             func(context.Context, Config, runtimePaths) (engineSession, error)
	database          func(Config, string) (*sql.DB, error)
	probe             func(context.Context, *sql.DB) error
	readinessInterval time.Duration
}

func Open(ctx context.Context, config Config) (*Runtime, error) {
	if config.LibraryPath == "" {
		located, err := LocateLibrary()
		if err != nil {
			return nil, err
		}
		config.LibraryPath = located
	}
	return open(ctx, config, launchOptions{
		start:             startEmbeddedEngine,
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
	paths, err := prepareRuntimePaths(config.DataDir)
	if err != nil {
		return nil, redactRuntimeError(config, err)
	}
	if options.start == nil || options.database == nil || options.probe == nil {
		return nil, errors.New("SeekDB engine start, database and readiness probe are required")
	}
	if options.readinessInterval <= 0 {
		return nil, errors.New("SeekDB readiness interval must be greater than zero")
	}

	clientLease, err := acquireSeekDBClientLease(paths)
	if err != nil {
		return nil, redactRuntimeError(config, fmt.Errorf("acquire SeekDB embedded client lease: %w", err))
	}
	engine, err := options.start(ctx, config, paths)
	if err != nil {
		_ = clientLease.Close()
		return nil, redactRuntimeError(config, fmt.Errorf("start SeekDB engine: %w", err))
	}

	bootstrapDatabase := config.Database
	if config.Database != seekDBBootstrapDatabase {
		bootstrapDatabase = seekDBBootstrapDatabase
	}
	database, err := options.database(config, bootstrapDatabase)
	if err != nil {
		_ = engine.Close(ctx)
		_ = clientLease.Close()
		return nil, redactRuntimeError(config, fmt.Errorf("configure SeekDB SQL connection: %w", err))
	}

	runtime := &Runtime{
		config: config, engine: engine, db: database, ctx: ctx,
		clientLease: clientLease, wait: make(chan struct{}),
	}

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
		case <-readyCtx.Done():
			closeCtx, closeCancel := context.WithTimeout(context.Background(), config.ShutdownLimit)
			_ = runtime.Close(closeCtx)
			closeCancel()
			return nil, redactRuntimeError(config, fmt.Errorf("wait for SeekDB readiness: %w", readyCtx.Err()))
		case <-ticker.C:
		}
	}
}

func startEmbeddedEngine(_ context.Context, config Config, _ runtimePaths) (engineSession, error) {
	if err := config.requireLibrary(); err != nil {
		return nil, err
	}
	if err := validateLibrary(config.LibraryPath); err != nil {
		return nil, err
	}
	if err := loadSeekDBLibrary(config.LibraryPath); err != nil {
		return nil, err
	}
	if err := engineOpen(config.DataDir); err != nil {
		return nil, err
	}
	return liveEngine{}, nil
}

type liveEngine struct{}

func (liveEngine) Close(context.Context) error {
	engineClose()
	return nil
}

type nopEngine struct{}

func (nopEngine) Close(context.Context) error { return nil }

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
		if !retainEmbeddedSQL(r.engine, database) {
			_ = database.Close()
		}
		return err
	}
	old := r.db
	r.db = database
	if retainEmbeddedSQL(r.engine, old) {
		return nil
	}
	if err := old.Close(); err != nil {
		_ = database.Close()
		r.db = old
		return err
	}
	return nil
}

var retainedSQL struct {
	mu  sync.Mutex
	dbs []*sql.DB
}

func retainSQLDatabase(database *sql.DB) {
	if database == nil {
		return
	}
	retainedSQL.mu.Lock()
	retainedSQL.dbs = append(retainedSQL.dbs, database)
	retainedSQL.mu.Unlock()
}

func retainEmbeddedSQL(engine engineSession, database *sql.DB) bool {
	if _, ok := engine.(liveEngine); !ok {
		return false
	}
	retainSQLDatabase(database)
	return true
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
		r.closeErr = r.closeEngine(ctx)
	})
	return r.closeErr
}

func (r *Runtime) closeEngine(ctx context.Context) error {
	r.mu.Lock()
	r.closing = true
	engine := r.engine
	database := r.db
	r.db = nil
	r.mu.Unlock()
	var databaseErr error
	if retainEmbeddedSQL(engine, database) {
		database = nil
	}
	if database != nil {
		databaseErr = database.Close()
	}
	var engineErr error
	if engine != nil {
		engineErr = engine.Close(ctx)
	}
	leaseErr := r.releaseClientLease()
	r.mu.Lock()
	r.waitErr = errors.Join(databaseErr, engineErr, leaseErr)
	r.mu.Unlock()
	select {
	case <-r.wait:
	default:
		close(r.wait)
	}
	return errors.Join(databaseErr, engineErr, leaseErr)
}

func (r *Runtime) releaseClientLease() error {
	if r == nil {
		return nil
	}
	r.leaseClose.Do(func() {
		if r.clientLease != nil {
			r.leaseErr = r.clientLease.Close()
		}
	})
	return r.leaseErr
}

func validateLibrary(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve SeekDB library: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("inspect SeekDB library: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("SeekDB library must be a regular file")
	}
	return nil
}

func prepareRuntimePaths(root string) (runtimePaths, error) {
	if err := ensurePrivateDirectory(root); err != nil {
		return runtimePaths{}, fmt.Errorf("prepare SeekDB data directory: %w", err)
	}
	paths := runtimePaths{
		Base: root,
		Data: filepath.Join(root, "store"),
		Redo: filepath.Join(root, "redo"),
		Run:  filepath.Join(root, "run"),
	}
	for _, path := range []string{paths.Data, paths.Redo, paths.Run} {
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
	return descriptor.Engine + "/" + descriptor.Database
}

func redactRuntimeError(config Config, err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if config.LibraryPath != "" {
		message = strings.ReplaceAll(message, config.LibraryPath, "[seekdb-library]")
	}
	if config.DataDir != "" {
		message = strings.ReplaceAll(message, config.DataDir, "[seekdb-data]")
	}
	return errors.New(message)
}
