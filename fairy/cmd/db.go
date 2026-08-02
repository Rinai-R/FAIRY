package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fairy/config"
	"fairy/coredb"
	"fairy/memory"
	"fairy/model"

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
	VectorRebuild(context.Context, int) (any, error)
}

type localDatabaseOperations struct {
	getenv func(string) string
}

type databaseStatusResult struct {
	DatabaseDescriptor coredb.Descriptor   `json:"database"`
	Schema             coredb.SchemaStatus `json:"schema"`
	Pool               coredb.PoolStats    `json:"pool"`
}

func newDBCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "db", Short: "Manage PostgreSQL", Args: cobra.NoArgs, GroupID: "admin"}
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
		Use: "status", Short: "Verify PostgreSQL schema", Args: cobra.NoArgs,
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
	command := &cobra.Command{Use: "vector", Short: "Manage PostgreSQL vector columns", Args: cobra.NoArgs}
	command.AddCommand(newDBVectorRebuildCmd(v, deps))
	return command
}

func newDBVectorRebuildCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	pageSize := defaultVectorPageSize
	command := &cobra.Command{
		Use: "rebuild", Short: "Rebuild PostgreSQL vectors from authoritative records", Args: cobra.NoArgs,
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		return nil, err
	}
	return coredb.VerifySchema(ctx, pool.Raw())
}

func (o localDatabaseOperations) Status(ctx context.Context) (any, error) {
	pool, err := o.openDatabase(ctx, true)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	schema, err := coredb.VerifySchema(ctx, pool.Raw())
	if err != nil {
		return nil, err
	}
	databaseDescriptor, err := pool.Config().Descriptor()
	if err != nil {
		return nil, err
	}
	return databaseStatusResult{
		DatabaseDescriptor: databaseDescriptor,
		Schema:             schema,
		Pool:               pool.Stats(),
	}, nil
}

func (o localDatabaseOperations) VectorRebuild(ctx context.Context, pageSize int) (any, error) {
	pool, err := o.openDatabase(ctx, true)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	cipher, err := config.SecretCipherFromEnv(o.getenv)
	if err != nil {
		return nil, fmt.Errorf("secret master key: %w", err)
	}
	secretStore, err := config.NewPostgresSecretStore(pool, cipher)
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
	embedder, err := model.NewModelService(root, secretStore).SemanticEmbedder(settings)
	if err != nil {
		return nil, fmt.Errorf("construct semantic embedder: %w", err)
	}
	store, err := memory.NewStoreFromPoolWithEmbedder(pool, embedder)
	if err != nil {
		return nil, err
	}
	return store.RebuildVectors(ctx, pageSize)
}

func (o localDatabaseOperations) openDatabase(ctx context.Context, verify bool) (*coredb.Pool, error) {
	databaseConfig, err := coredb.ConfigFromEnv(o.getenv)
	if err != nil {
		return nil, fmt.Errorf("database configuration: %w", err)
	}
	pool, err := coredb.Open(ctx, databaseConfig)
	if err != nil {
		return nil, err
	}
	if verify {
		if _, err := coredb.VerifySchema(ctx, pool.Raw()); err != nil {
			pool.Close()
			return nil, fmt.Errorf("database schema: %w", err)
		}
	}
	return pool, nil
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
	return filepath.Join(home, "Library", "Application Support", "dev.rinai.fairy", "session-core", "v1"), nil
}
