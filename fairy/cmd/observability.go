package cmd

import (
	"errors"
	"fmt"
	"io"

	"fairy/coreclient"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newLogsCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	var follow bool
	var level, logger string
	var afterSequence uint64
	var limit int
	command := &cobra.Command{
		Use: "logs", Short: "Query or follow structured Core logs", Args: cobra.NoArgs, GroupID: "debug",
		RunE: func(command *cobra.Command, args []string) error {
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			query := coreclient.LogQuery{Level: level, LoggerPrefix: logger, AfterSequence: afterSequence, Limit: limit}
			if !follow {
				result, err := client.Logs(command.Context(), query)
				if err != nil {
					return err
				}
				return writeOutput(command.OutOrStdout(), config.Output, result)
			}
			query.Limit = 0
			stream, err := client.OpenLogs(command.Context(), query, config.Timeout)
			if err != nil {
				return err
			}
			defer stream.Close()
			for {
				event, err := stream.Next()
				if err != nil {
					if command.Context().Err() != nil || errors.Is(err, io.EOF) && command.Context().Err() != nil {
						return nil
					}
					return fmt.Errorf("log stream disconnected: %w", err)
				}
				entry, err := coreclient.DecodeLogEntry(event)
				if err != nil {
					return err
				}
				if err := writeJSONLine(command.OutOrStdout(), entry); err != nil {
					return err
				}
			}
		},
	}
	command.Flags().BoolVar(&follow, "follow", false, "follow live logs as JSONL")
	command.Flags().StringVar(&level, "level", "", "minimum level: debug, info, warn, error")
	command.Flags().StringVar(&logger, "logger", "", "logger prefix")
	command.Flags().Uint64Var(&afterSequence, "after-sequence", 0, "only return later sequence values")
	command.Flags().IntVar(&limit, "limit", 0, "query result limit (1-500)")
	return command
}

func newMetricsCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "metrics", Short: "Read runtime metrics", Args: cobra.NoArgs, GroupID: "debug",
		RunE: func(command *cobra.Command, args []string) error {
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			metrics, err := client.Metrics(command.Context())
			if err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), config.Output, metrics)
		},
	}
}
