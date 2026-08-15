package cmd

import (
	"encoding/hex"
	"fmt"
	"path/filepath"

	"fairy/plugin"

	"github.com/spf13/cobra"
)

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "plugin",
		Short:   "Validate and pack local WASM plugin packages",
		GroupID: "debug",
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(newPluginValidateCmd(), newPluginPackCmd())
	return cmd
}

func newPluginValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate PATH",
		Short: "Validate a plugin directory or .fairy-plugin archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			bundle, err := plugin.ValidatePath(args[0])
			if err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), "json", pluginReport(bundle))
		},
	}
}

func newPluginPackCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "pack DIR",
		Short: "Pack a plugin directory into a .fairy-plugin archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			dir := args[0]
			if output == "" {
				output = filepath.Join(dir, "plugin.fairy-plugin")
			}
			bundle, err := plugin.PackDir(dir, output)
			if err != nil {
				return err
			}
			report := pluginReport(bundle)
			report["path"] = output
			return writeOutput(command.OutOrStdout(), "json", report)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output .fairy-plugin path")
	return cmd
}

func pluginReport(bundle plugin.Bundle) map[string]any {
	return map[string]any{
		"id":      bundle.Manifest.ID,
		"version": bundle.Manifest.Version,
		"abi":     fmt.Sprintf("%d-%d", bundle.Manifest.ABI.Min, bundle.Manifest.ABI.Max),
		"sha256":  hex.EncodeToString(bundle.SHA256[:]),
	}
}
