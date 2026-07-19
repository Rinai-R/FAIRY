package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

func newCompletionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use: "completion <bash|zsh|fish|powershell>", Short: "Generate shell completion", Args: cobra.ExactArgs(1), GroupID: "shell",
		RunE: func(command *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(command.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(command.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(command.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(command.OutOrStdout())
			default:
				return errors.New("shell must be bash, zsh, fish, or powershell")
			}
		},
	}
}
