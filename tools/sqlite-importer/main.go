package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"fairy-sqlite-importer/importer"
	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use: "fairy-sqlite-importer", Short: "Offline FAIRY SQLite migration tool",
		Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
	}
	root.AddCommand(newPreflightCmd())
	root.AddCommand(newRunCmd())
	return root
}

func newRunCmd() *cobra.Command {
	var intelligencePath, secretPath string
	command := &cobra.Command{
		Use: "run", Short: "Import, resume, or re-verify one immutable SQLite source", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			report, err := importer.Run(command.Context(), importer.RunOptions{
				IntelligencePath: intelligencePath, SecretPath: secretPath, Getenv: os.Getenv,
			})
			if err != nil {
				return err
			}
			return importer.WriteJSON(command.OutOrStdout(), report)
		},
	}
	command.Flags().StringVar(&intelligencePath, "intelligence", "", "absolute path to legacy intelligence SQLite database")
	command.Flags().StringVar(&secretPath, "secrets", "", "absolute path to legacy secret SQLite database")
	_ = command.MarkFlagRequired("intelligence")
	return command
}

func newPreflightCmd() *cobra.Command {
	var intelligencePath, secretPath string
	command := &cobra.Command{
		Use: "preflight", Short: "Validate immutable SQLite sources and empty production targets", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			report, err := importer.Preflight(command.Context(), importer.PreflightOptions{
				IntelligencePath: intelligencePath,
				SecretPath:       secretPath,
				Getenv:           os.Getenv,
			})
			if err != nil {
				return err
			}
			return importer.WriteJSON(command.OutOrStdout(), report)
		},
	}
	command.Flags().StringVar(&intelligencePath, "intelligence", "", "absolute path to legacy intelligence SQLite database")
	command.Flags().StringVar(&secretPath, "secrets", "", "absolute path to legacy secret SQLite database")
	_ = command.MarkFlagRequired("intelligence")
	return command
}
