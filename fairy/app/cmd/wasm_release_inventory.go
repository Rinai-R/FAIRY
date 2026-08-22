package cmd

import (
	"errors"
	"strings"

	"fairy/runtime/wasm"

	"github.com/spf13/cobra"
)

// newWASMReleaseInventoryCmd is a build-only installer/verifier. Runtime users
// install their own packages through the plugin API; the Desktop release recipe
// uses this command only for explicitly cataloged built-in artifacts.
func newWASMReleaseInventoryCmd() *cobra.Command {
	var inventoryPath string
	var sourceRoot string
	var outputPath string
	command := &cobra.Command{
		Use:    "wasm-release-inventory",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			for _, value := range []string{inventoryPath, sourceRoot, outputPath} {
				if value == "" || value != strings.TrimSpace(value) {
					return errors.New("WASM inventory, source root, and output paths are required")
				}
			}
			return wasm.InstallReleaseInventory(command.Context(), inventoryPath, sourceRoot, outputPath)
		},
	}
	command.Flags().StringVar(&inventoryPath, "inventory", "", "path to the release plugin inventory")
	command.Flags().StringVar(&sourceRoot, "root", "", "root containing cataloged package and license files")
	command.Flags().StringVar(&outputPath, "output", "", "new destination for sealed package artifacts and installation evidence")
	return command
}
