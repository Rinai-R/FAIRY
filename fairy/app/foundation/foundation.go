// Package foundation opens FAIRY's local authoritative storage boundary.
//
// It intentionally stops before constructing Session and Turn services or
// other unmigrated domains. Those domains join this composition only after
// their stores have moved to SeekDB; until then this package cannot accidentally
// make a partially migrated Core look ready.
package foundation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"fairy/context/character"
	"fairy/context/identity"
	"fairy/runtime/config"
	"fairy/runtime/seekdb"
)

var (
	ErrLifetimeContextRequired = errors.New("foundation lifetime context is required")
	ErrCharacterRootRequired   = errors.New("foundation character root is required")
	ErrSeekDBUnavailable       = errors.New("foundation SeekDB connection is unavailable")
	ErrSchemaNotReady          = errors.New("foundation SeekDB schema is not current")
	ErrFoundationClosed        = errors.New("foundation is closed")
)

// Options identifies local non-database character assets and the environment
// source used for SeekDB configuration. Getenv exists so launchers and tests can
// provide an explicit configuration boundary; nil uses the process environment.
type Options struct {
	CharacterRoot string
	Getenv        func(string) string
}

// Status is the credential-free readiness projection safe for local UI and
// diagnostics. SecretsReady means the local master key was validated and the
// cipher was constructed during this foundation lifetime; it is not a live
// filesystem repair check. The projection never contains a connection string,
// filesystem path, or master-key detail.
type Status struct {
	Storage      string              `json:"storage"`
	SeekDB       seekdb.Descriptor   `json:"seekdb"`
	Schema       seekdb.SchemaStatus `json:"schema"`
	SecretsReady bool                `json:"secretsReady"`
}

// Foundation owns the local SeekDB runtime and the stores whose schemas have
// already migrated. SQL is exposed only as the concrete database/sql boundary
// needed by later domain migration tasks.
type Foundation struct {
	Documents  *config.DocumentStore
	Secrets    *config.SecretStore
	Profile    *config.ProfileStore
	Identity   *identity.Store
	Characters *character.Store

	runtime       runtimeBoundary
	database      *sql.DB
	queryLimit    time.Duration
	shutdownLimit time.Duration
	status        Status
	checkSchema   func(context.Context, *sql.DB, seekdb.Revision) (seekdb.SchemaStatus, error)

	mu       sync.RWMutex
	closed   bool
	close    sync.Once
	closeErr error
}

type runtimeBoundary interface {
	SQL() *sql.DB
	Descriptor() seekdb.Descriptor
	Close(context.Context) error
}

type foundationOperations struct {
	configFromEnv func(func(string) string) (seekdb.Config, error)
	openRuntime   func(context.Context, seekdb.Config) (runtimeBoundary, error)
	migrate       func(context.Context, *sql.DB, []seekdb.Migration) error
	checkSchema   func(context.Context, *sql.DB, seekdb.Revision) (seekdb.SchemaStatus, error)
	openCipher    func(string) (*config.SecretCipher, error)
}

var productionOperations = foundationOperations{
	configFromEnv: seekdb.ConfigFromEnv,
	openRuntime: func(ctx context.Context, runtimeConfig seekdb.Config) (runtimeBoundary, error) {
		return seekdb.Open(ctx, runtimeConfig)
	},
	migrate:     seekdb.MigrateSchema,
	checkSchema: seekdb.CheckSchema,
	openCipher:  config.SecretCipherFromDataDir,
}

// Open starts local SeekDB, applies the immutable migration chain, performs a
// separate read-only current-revision check, loads the local master key, and
// constructs only the already-migrated foundation stores. Any failure closes
// the owned runtime; there is no legacy remote-database or alternate local
// storage fallback.
//
// lifetime must outlive the returned Foundation. SeekDB already applies its own
// bounded StartLimit; canceling lifetime terminates the owned process.
func Open(lifetime context.Context, options Options) (*Foundation, error) {
	return open(lifetime, options, productionOperations)
}

func open(lifetime context.Context, options Options, operations foundationOperations) (_ *Foundation, returnErr error) {
	if lifetime == nil {
		return nil, ErrLifetimeContextRequired
	}
	if options.CharacterRoot == "" {
		return nil, ErrCharacterRootRequired
	}
	characterRoot, err := character.ValidateVisualRoot(options.CharacterRoot)
	if err != nil {
		return nil, err
	}
	if err := validateOperations(operations); err != nil {
		return nil, err
	}

	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	runtimeConfig, err := operations.configFromEnv(getenv)
	if err != nil {
		return nil, fmt.Errorf("foundation SeekDB configuration: %w", err)
	}
	localRuntime, err := operations.openRuntime(lifetime, runtimeConfig)
	if err != nil {
		return nil, fmt.Errorf("opening foundation SeekDB: %w", err)
	}
	if localRuntime == nil {
		return nil, ErrSeekDBUnavailable
	}
	keepRuntime := false
	defer func() {
		if keepRuntime {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), runtimeConfig.ShutdownLimit)
		defer cancel()
		returnErr = errors.Join(returnErr, localRuntime.Close(closeCtx))
	}()

	database := localRuntime.SQL()
	if database == nil {
		return nil, ErrSeekDBUnavailable
	}
	migrationCtx, cancelMigration := context.WithTimeout(lifetime, max(runtimeConfig.StartLimit, runtimeConfig.QueryLimit))
	err = operations.migrate(migrationCtx, database, seekdb.BuiltinMigrations())
	cancelMigration()
	if err != nil {
		return nil, fmt.Errorf("migrating foundation SeekDB schema: %w", err)
	}

	readinessCtx, cancelReadiness := context.WithTimeout(lifetime, runtimeConfig.QueryLimit)
	schemaStatus, err := operations.checkSchema(readinessCtx, database, seekdb.CurrentSchemaRevision())
	cancelReadiness()
	if err != nil {
		return nil, fmt.Errorf("checking foundation SeekDB schema: %w", errors.Join(ErrSchemaNotReady, err))
	}
	if schemaStatus.State != seekdb.SchemaCurrent {
		return nil, fmt.Errorf("%w: state %q", ErrSchemaNotReady, schemaStatus.State)
	}

	cipher, err := operations.openCipher(runtimeConfig.DataDir)
	if err != nil {
		return nil, fmt.Errorf("opening foundation local master key: %w", err)
	}
	secretStore, err := config.NewSeekDBSecretStore(database, cipher, runtimeConfig.QueryLimit)
	if err != nil {
		return nil, fmt.Errorf("constructing foundation secret store: %w", err)
	}
	documentStore, err := config.NewSeekDBDocumentStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		return nil, fmt.Errorf("constructing foundation config store: %w", err)
	}
	profileStore, err := config.NewSeekDBProfileStore(documentStore)
	if err != nil {
		return nil, fmt.Errorf("constructing foundation profile store: %w", err)
	}
	identityStore, err := identity.NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		return nil, fmt.Errorf("constructing foundation identity store: %w", err)
	}
	characterStore, err := character.NewSeekDBStore(database, characterRoot, runtimeConfig.QueryLimit)
	if err != nil {
		return nil, fmt.Errorf("constructing foundation character store: %w", err)
	}

	foundation := &Foundation{
		Documents:     documentStore,
		Secrets:       secretStore,
		Profile:       profileStore,
		Identity:      identityStore,
		Characters:    characterStore,
		runtime:       localRuntime,
		database:      database,
		queryLimit:    runtimeConfig.QueryLimit,
		shutdownLimit: runtimeConfig.ShutdownLimit,
		status: Status{
			Storage:      "seekdb",
			SeekDB:       localRuntime.Descriptor(),
			Schema:       schemaStatus,
			SecretsReady: secretStore.Encrypted(),
		},
		checkSchema: operations.checkSchema,
	}
	keepRuntime = true
	return foundation, nil
}

func validateOperations(operations foundationOperations) error {
	if operations.configFromEnv == nil || operations.openRuntime == nil || operations.migrate == nil || operations.checkSchema == nil || operations.openCipher == nil {
		return errors.New("foundation operations are incomplete")
	}
	return nil
}

// SQL returns the concrete SeekDB connection pool while the foundation is
// open. Domain packages accept this value directly instead of a dialect facade.
func (f *Foundation) SQL() (*sql.DB, error) {
	if f == nil {
		return nil, ErrFoundationClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed || f.database == nil {
		return nil, ErrFoundationClosed
	}
	return f.database, nil
}

func (f *Foundation) QueryLimit() time.Duration {
	if f == nil {
		return 0
	}
	return f.queryLimit
}

func (f *Foundation) Status(ctx context.Context) (Status, error) {
	if f == nil {
		return Status{}, ErrFoundationClosed
	}
	if ctx == nil {
		return Status{}, errors.New("foundation status context is required")
	}
	f.mu.RLock()
	closed := f.closed
	database := f.database
	status := f.status
	checkSchema := f.checkSchema
	queryLimit := f.queryLimit
	f.mu.RUnlock()
	if closed || database == nil {
		return Status{}, ErrFoundationClosed
	}
	readinessCtx, cancel := context.WithTimeout(ctx, queryLimit)
	defer cancel()
	schemaStatus, err := checkSchema(readinessCtx, database, seekdb.CurrentSchemaRevision())
	if err != nil {
		return Status{}, fmt.Errorf("checking foundation SeekDB status: %w", errors.Join(ErrSchemaNotReady, err))
	}
	if schemaStatus.State != seekdb.SchemaCurrent {
		return Status{}, fmt.Errorf("%w: state %q", ErrSchemaNotReady, schemaStatus.State)
	}
	status.Schema = schemaStatus
	return status, nil
}

// Close releases the owned runtime exactly once. The SQL pool belongs to that
// runtime and is never closed separately.
func (f *Foundation) Close(ctx context.Context) error {
	if f == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("foundation close context is required")
	}
	f.close.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.database = nil
		f.mu.Unlock()
		if f.runtime != nil {
			closeCtx, cancel := boundedCloseContext(ctx, f.shutdownLimit)
			defer cancel()
			f.closeErr = f.runtime.Close(closeCtx)
		}
	})
	return f.closeErr
}

func boundedCloseContext(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= limit {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, limit)
}

func (f *Foundation) ShutdownLimit() time.Duration {
	if f == nil {
		return 0
	}
	return f.shutdownLimit
}
