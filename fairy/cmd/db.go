package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fairy/config"
	"fairy/memory"
	"fairy/model"
	pgstore "fairy/postgres"
	"fairy/secret"
	"fairy/vectorindex"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	defaultVectorPageSize = 100
	maxVectorPageSize     = 100
)

type DatabaseOperations interface {
	Migrate(context.Context) (any, error)
	Status(context.Context) (any, error)
	VectorMigrate(context.Context) (any, error)
	VectorRebuild(context.Context, int) (any, error)
	VectorReconcile(context.Context, bool) (any, error)
}

type localDatabaseOperations struct {
	getenv func(string) string
}

type databaseStatusResult struct {
	DatabaseDescriptor pgstore.Descriptor           `json:"database"`
	Schema             pgstore.SchemaStatus         `json:"schema"`
	Pool               pgstore.PoolStats            `json:"pool"`
	QdrantDescriptor   vectorindex.Descriptor       `json:"qdrant"`
	Collection         vectorindex.CollectionStatus `json:"collection"`
}

func newDBCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "db", Short: "Manage PostgreSQL and Qdrant", Args: cobra.NoArgs, GroupID: "admin"}
	command.AddCommand(
		newDBMigrateCmd(v, deps),
		newDBStatusCmd(v, deps),
		newDBVectorCmd(v, deps),
	)
	return command
}

func newDBMigrateCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "migrate", Short: "Create the current PostgreSQL schema with GORM", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			result, err := deps.Database.Migrate(command.Context())
			if err != nil {
				return err
			}
			return writeDatabaseOutput(command, v, result)
		},
	}
}

func newDBStatusCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Verify PostgreSQL schema and Qdrant collection", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			result, err := deps.Database.Status(command.Context())
			if err != nil {
				return err
			}
			return writeDatabaseOutput(command, v, result)
		},
	}
}

func newDBVectorCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "vector", Short: "Manage the derived Qdrant vector index", Args: cobra.NoArgs}
	command.AddCommand(
		&cobra.Command{
			Use: "migrate", Short: "Create or verify the Qdrant collection", Args: cobra.NoArgs,
			RunE: func(command *cobra.Command, args []string) error {
				result, err := deps.Database.VectorMigrate(command.Context())
				if err != nil {
					return err
				}
				return writeDatabaseOutput(command, v, result)
			},
		},
		newDBVectorRebuildCmd(v, deps),
		newDBVectorReconcileCmd(v, deps),
	)
	return command
}

func newDBVectorRebuildCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	pageSize := defaultVectorPageSize
	command := &cobra.Command{
		Use: "rebuild", Short: "Rebuild Qdrant from authoritative PostgreSQL items", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if pageSize < 1 || pageSize > maxVectorPageSize {
				return fmt.Errorf("page-size must be between 1 and %d", maxVectorPageSize)
			}
			result, err := deps.Database.VectorRebuild(command.Context(), pageSize)
			if err != nil {
				return err
			}
			return writeDatabaseOutput(command, v, result)
		},
	}
	command.Flags().IntVar(&pageSize, "page-size", defaultVectorPageSize, "authoritative PostgreSQL items per page (1-100)")
	return command
}

func newDBVectorReconcileCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	apply := false
	command := &cobra.Command{
		Use: "reconcile", Short: "Report vector drift or explicitly delete verified orphans", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			result, err := deps.Database.VectorReconcile(command.Context(), apply)
			if err != nil {
				return err
			}
			return writeDatabaseOutput(command, v, result)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "delete points confirmed as orphans after authoritative re-check")
	return command
}

func writeDatabaseOutput(command *cobra.Command, v *viper.Viper, result any) error {
	format := v.GetString("output")
	if format != "json" && format != "table" {
		return errors.New("output must be json or table")
	}
	return writeOutput(command.OutOrStdout(), format, result)
}

func (o localDatabaseOperations) Migrate(ctx context.Context) (any, error) {
	pool, err := o.openDatabase(ctx, false)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		return nil, err
	}
	return pgstore.VerifySchema(ctx, pool)
}

func (o localDatabaseOperations) Status(ctx context.Context) (any, error) {
	pool, err := o.openDatabase(ctx, true)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	schema, err := pgstore.VerifySchema(ctx, pool)
	if err != nil {
		return nil, err
	}
	databaseDescriptor, err := pool.Config().Descriptor()
	if err != nil {
		return nil, err
	}
	index, err := o.openVector(ctx, true)
	if err != nil {
		return nil, err
	}
	defer index.Close()
	collection, err := index.VerifyCollection(ctx)
	if err != nil {
		return nil, err
	}
	qdrantDescriptor, err := index.Descriptor()
	if err != nil {
		return nil, err
	}
	return databaseStatusResult{
		DatabaseDescriptor: databaseDescriptor,
		Schema:             schema,
		Pool:               pool.Stats(),
		QdrantDescriptor:   qdrantDescriptor,
		Collection:         collection,
	}, nil
}

func (o localDatabaseOperations) VectorMigrate(ctx context.Context) (any, error) {
	index, err := o.openVector(ctx, false)
	if err != nil {
		return nil, err
	}
	defer index.Close()
	if err := index.MigrateCollection(ctx); err != nil {
		return nil, err
	}
	return index.VerifyCollection(ctx)
}

func (o localDatabaseOperations) VectorRebuild(ctx context.Context, pageSize int) (any, error) {
	pool, store, index, err := o.openMaintenanceDependencies(ctx)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	defer index.Close()
	cipher, err := secret.CipherFromEnv(o.getenv)
	if err != nil {
		return nil, fmt.Errorf("secret master key: %w", err)
	}
	secretStore, err := secret.NewPostgresStore(pool, cipher)
	if err != nil {
		return nil, err
	}
	root, err := o.configRoot()
	if err != nil {
		return nil, err
	}
	settings, err := config.ReadSemanticEmbeddingSettings(root)
	if err != nil {
		return nil, err
	}
	embedder, err := model.NewModelService(root, secretStore).SemanticAPIEmbedder(settings)
	if err != nil {
		return nil, fmt.Errorf("construct semantic embedder: %w", err)
	}
	return store.RebuildVectorIndex(ctx, embedder, index, pageSize)
}

func (o localDatabaseOperations) VectorReconcile(ctx context.Context, apply bool) (any, error) {
	pool, store, index, err := o.openMaintenanceDependencies(ctx)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	defer index.Close()
	return store.ReconcileVectorIndex(ctx, index, apply)
}

func (o localDatabaseOperations) openMaintenanceDependencies(ctx context.Context) (*pgstore.Pool, *memory.Store, *vectorindex.Client, error) {
	pool, err := o.openDatabase(ctx, true)
	if err != nil {
		return nil, nil, nil, err
	}
	store, err := memory.NewStoreFromPool(pool)
	if err != nil {
		pool.Close()
		return nil, nil, nil, err
	}
	index, err := o.openVector(ctx, true)
	if err != nil {
		pool.Close()
		return nil, nil, nil, err
	}
	return pool, store, index, nil
}

func (o localDatabaseOperations) openDatabase(ctx context.Context, verify bool) (*pgstore.Pool, error) {
	databaseConfig, err := pgstore.ConfigFromEnv(o.getenv)
	if err != nil {
		return nil, fmt.Errorf("database configuration: %w", err)
	}
	pool, err := pgstore.Open(ctx, databaseConfig)
	if err != nil {
		return nil, err
	}
	if verify {
		if _, err := pgstore.VerifySchema(ctx, pool); err != nil {
			pool.Close()
			return nil, fmt.Errorf("database schema: %w", err)
		}
	}
	return pool, nil
}

func (o localDatabaseOperations) openVector(ctx context.Context, verify bool) (*vectorindex.Client, error) {
	vectorConfig, err := vectorindex.ConfigFromEnv(o.getenv)
	if err != nil {
		return nil, fmt.Errorf("qdrant configuration: %w", err)
	}
	index, err := vectorindex.Open(ctx, vectorConfig)
	if err != nil {
		return nil, err
	}
	if verify {
		if _, err := index.VerifyCollection(ctx); err != nil {
			index.Close()
			return nil, fmt.Errorf("qdrant collection: %w", err)
		}
	}
	return index, nil
}

func (o localDatabaseOperations) configRoot() (string, error) {
	root := o.getenv("FAIRY_CONFIG_ROOT")
	if root != strings.TrimSpace(root) {
		return "", errors.New("FAIRY_CONFIG_ROOT must not contain leading or trailing whitespace")
	}
	if root != "" {
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "dev.rinai.fairy", "harness", "v1"), nil
}
