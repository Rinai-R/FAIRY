package cmd

import (
	"context"
	"errors"
	"fmt"

	"fairy/runtime/seekdb"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type DatabaseOperations interface {
	Migrate(context.Context) (any, error)
	Status(context.Context) (any, error)
}

type localDatabaseOperations struct {
	getenv func(string) string
}

type databaseStatusResult struct {
	Descriptor seekdb.Descriptor   `json:"descriptor"`
	Schema     seekdb.SchemaStatus `json:"schema"`
	Storage    string              `json:"storage"`
}

func newDBCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "db", Short: "Manage local SeekDB", Args: cobra.NoArgs, GroupID: "admin"}
	command.AddCommand(
		newDBMigrateCmd(v, deps),
		newDBStatusCmd(v, deps),
	)
	return command
}

func newDBMigrateCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "migrate", Short: "Apply the current SeekDB schema", Args: cobra.NoArgs,
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
		Use: "status", Short: "Verify the current SeekDB schema", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			result, err := deps.Database.Status(command.Context())
			if err != nil {
				return err
			}
			return writeDatabaseOutput(command, v, result)
		},
	}
}

func writeDatabaseOutput(command *cobra.Command, v *viper.Viper, result any) error {
	format := v.GetString("output")
	if format != "json" && format != "table" {
		return errors.New("output must be json or table")
	}
	return writeOutput(command.OutOrStdout(), format, result)
}

func (o localDatabaseOperations) Migrate(ctx context.Context) (any, error) {
	return o.withRuntime(ctx, func(ctx context.Context, runtime *seekdb.Runtime, config seekdb.Config) (any, error) {
		if err := seekdb.MigrateSchema(ctx, runtime.SQL(), seekdb.BuiltinMigrations()); err != nil {
			return nil, err
		}
		return o.schemaStatus(ctx, runtime, config)
	})
}

func (o localDatabaseOperations) Status(ctx context.Context) (any, error) {
	return o.withRuntime(ctx, func(ctx context.Context, runtime *seekdb.Runtime, config seekdb.Config) (any, error) {
		return o.schemaStatus(ctx, runtime, config)
	})
}

func (o localDatabaseOperations) schemaStatus(ctx context.Context, runtime *seekdb.Runtime, config seekdb.Config) (any, error) {
	schema, err := seekdb.CheckSchema(ctx, runtime.SQL(), seekdb.CurrentSchemaRevision())
	if err != nil {
		return nil, err
	}
	return databaseStatusResult{
		Descriptor: config.Descriptor(),
		Schema:     schema,
		Storage:    "seekdb",
	}, nil
}

func (o localDatabaseOperations) withRuntime(
	ctx context.Context,
	fn func(context.Context, *seekdb.Runtime, seekdb.Config) (any, error),
) (any, error) {
	config, err := seekdb.ConfigFromEnv(o.getenv)
	if err != nil {
		return nil, fmt.Errorf("SeekDB configuration: %w", err)
	}
	runtime, err := seekdb.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownLimit)
		defer cancel()
		_ = runtime.Close(closeCtx)
	}()
	return fn(ctx, runtime, config)
}
