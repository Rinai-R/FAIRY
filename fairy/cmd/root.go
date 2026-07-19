package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "fairy",
		Short: "FAIRY Session Core — companion agent provider",
		Long:  "FAIRY Session Core. Primary Surface entrypoint is `fairy serve` (Hertz HTTP/SSE). Other CLI commands are for local debugging.",
	}
	root.PersistentFlags().String("config-root", "", "config root (default: FAIRY_CONFIG_ROOT or macOS Application Support path)")
	root.AddCommand(newServeCmd())
	root.AddCommand(newSessionCmd())
	root.AddCommand(newTurnCmd())
	root.AddCommand(newCancelCmd())
	root.AddCommand(newStatusCmd())
	return root
}

func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func configRootFlag(cmd *cobra.Command) (string, error) {
	return cmd.Root().PersistentFlags().GetString("config-root")
}
