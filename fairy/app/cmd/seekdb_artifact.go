package cmd

import (
	"errors"
	"runtime"
	"strings"

	"fairy/runtime/seekdb"

	"github.com/spf13/cobra"
)

// newSeekDBArtifactCmd is a build-only verifier. It stays hidden from the
// end-user CLI because release packaging, not a running FAIRY instance, owns
// artifact acquisition and redistribution.
func newSeekDBArtifactCmd() *cobra.Command {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	var bundle seekdb.ArtifactBundle
	command := &cobra.Command{
		Use:    "seekdb-artifact",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			catalog, err := seekdb.BuiltinArtifactCatalog()
			if err != nil {
				return err
			}
			// Keep target verification first: a candidate or unsupported target
			// must report the release blocker rather than a coincidental missing
			// local file.
			if _, err := catalog.Verified(goos, goarch); err != nil {
				return err
			}
			for _, filename := range []string{bundle.ArchivePath, bundle.LicensePath, bundle.NoticePath} {
				if filename == "" || filename != strings.TrimSpace(filename) {
					return errors.New("SeekDB archive, LICENSE, and NOTICE build input paths are required")
				}
			}
			return catalog.VerifyBundle(goos, goarch, bundle)
		},
	}
	command.Flags().StringVar(&goos, "goos", goos, "target GOOS")
	command.Flags().StringVar(&goarch, "goarch", goarch, "target GOARCH")
	command.Flags().StringVar(&bundle.ArchivePath, "archive", "", "path to the fixed SeekDB release archive")
	command.Flags().StringVar(&bundle.LicensePath, "license", "", "path to the fixed-tag SeekDB LICENSE")
	command.Flags().StringVar(&bundle.NoticePath, "notice", "", "path to the fixed-tag SeekDB NOTICE")
	return command
}
