package cmd

import (
	"errors"
	"strings"

	"fairy/core"
	"github.com/spf13/cobra"
)

func newServeCmd(deps Dependencies) *cobra.Command {
	var options core.Options
	command := &cobra.Command{
		Use:     "serve",
		Short:   "Start the Session Core HTTP/SSE server",
		Args:    cobra.NoArgs,
		GroupID: "core",
		RunE: func(command *cobra.Command, args []string) error {
			environment := serveOptionsFromEnvironment(deps.Getenv)
			if options.ConfigRoot == "" {
				options.ConfigRoot = environment.ConfigRoot
			}
			if options.Addr == "" {
				options.Addr = environment.Addr
			}
			options.Token = environment.Token
			return runServe(command, deps, options)
		},
	}
	command.Flags().StringVar(&options.ConfigRoot, "config-root", "", "configuration root")
	command.Flags().StringVar(&options.Addr, "addr", "", "listen address")
	return command
}

func serveOptionsFromEnvironment(getenv func(string) string) core.Options {
	addr := strings.TrimSpace(getenv("FAIRY_LISTEN_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	return core.Options{
		ConfigRoot: strings.TrimSpace(getenv("FAIRY_CONFIG_ROOT")),
		Addr:       addr,
		Token:      getenv("FAIRY_API_TOKEN"),
	}
}

func runServe(command *cobra.Command, deps Dependencies, options core.Options) error {
	if options.Token != strings.TrimSpace(options.Token) {
		return errors.New("API token must not contain leading or trailing whitespace")
	}
	return deps.Serve(command.Context(), options)
}
