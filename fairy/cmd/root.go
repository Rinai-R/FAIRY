// Package cmd defines the FAIRY command-line routing layer.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"fairy/core"
	"fairy/coreclient"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type APIClient interface {
	Status(context.Context) (coreclient.Status, error)
	OpenSession(context.Context, coreclient.OpenSessionRequest) (coreclient.OpenSessionResponse, error)
	DecideParticipation(context.Context, string, coreclient.ParticipationRequest) (coreclient.ParticipationResponse, error)
	SubmitTurn(context.Context, string, coreclient.SubmitTurnRequest) (coreclient.SubmitTurnResponse, error)
	CancelTurn(context.Context, string, string) error
	OpenEvents(context.Context, string, time.Duration) (coreclient.EventStream, error)
	GetConfig(context.Context, string) (json.RawMessage, error)
	ApplyConfig(context.Context, string, []byte) (json.RawMessage, error)
	DeleteConfig(context.Context, string) (json.RawMessage, error)
	GetProfile(context.Context) (json.RawMessage, error)
	ApplyProfile(context.Context, []byte) (json.RawMessage, error)
	DeleteProfile(context.Context) (json.RawMessage, error)
	ListCharacters(context.Context) (coreclient.CharacterCatalog, error)
	CreateCharacter(context.Context, []byte) (json.RawMessage, error)
	ActivateCharacter(context.Context, string, uint64) (json.RawMessage, error)
	Logs(context.Context, coreclient.LogQuery) (coreclient.LogResponse, error)
	OpenLogs(context.Context, coreclient.LogQuery, time.Duration) (coreclient.EventStream, error)
	Metrics(context.Context) (coreclient.Metrics, error)
	ListOwnerIdentities(context.Context) ([]coreclient.OwnerIdentity, error)
	BindOwnerIdentity(context.Context, string, string) (coreclient.OwnerIdentity, error)
	UnbindOwnerIdentity(context.Context, string, string) error
}

type ConnectionConfig struct {
	Endpoint string
	Timeout  time.Duration
	Output   string
	Token    string
}

type Dependencies struct {
	Getenv        func(string) string
	Stdin         io.Reader
	ClientFactory func(ConnectionConfig) (APIClient, error)
	Serve         func(context.Context, core.Options) error
	Database      DatabaseOperations
}

func DefaultDependencies() Dependencies {
	return Dependencies{
		Getenv: os.Getenv,
		Stdin:  os.Stdin,
		ClientFactory: func(config ConnectionConfig) (APIClient, error) {
			return coreclient.New(coreclient.Options{
				Endpoint: config.Endpoint, Timeout: config.Timeout, Token: config.Token,
			})
		},
		Serve:    core.Run,
		Database: localDatabaseOperations{getenv: os.Getenv},
	}
}

func NewRootCmd(dependencies Dependencies) *cobra.Command {
	deps := normalizeDependencies(dependencies)
	v := viper.New()
	v.SetDefault("endpoint", coreclient.DefaultEndpoint)
	v.SetDefault("timeout", coreclient.DefaultTimeout)
	v.SetDefault("output", "json")
	_ = v.BindEnv("endpoint", "FAIRY_ENDPOINT")
	_ = v.BindEnv("timeout", "FAIRY_CLI_TIMEOUT")
	_ = v.BindEnv("output", "FAIRY_CLI_OUTPUT")

	root := &cobra.Command{
		Use:           "fairy",
		Short:         "FAIRY Session Core server and debugging client",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, args []string) error {
			return runServe(command, deps, serveOptionsFromEnvironment(deps.Getenv))
		},
	}
	root.AddGroup(
		&cobra.Group{ID: "core", Title: "Core commands:"},
		&cobra.Group{ID: "debug", Title: "Debug commands:"},
		&cobra.Group{ID: "admin", Title: "Admin commands:"},
		&cobra.Group{ID: "shell", Title: "Completion commands:"},
	)
	root.PersistentFlags().String("endpoint", coreclient.DefaultEndpoint, "Session Core HTTP endpoint")
	root.PersistentFlags().Duration("timeout", coreclient.DefaultTimeout, "finite request and SSE ready timeout")
	root.PersistentFlags().StringP("output", "o", "json", "output format: json or table")
	_ = v.BindPFlag("endpoint", root.PersistentFlags().Lookup("endpoint"))
	_ = v.BindPFlag("timeout", root.PersistentFlags().Lookup("timeout"))
	_ = v.BindPFlag("output", root.PersistentFlags().Lookup("output"))

	root.AddCommand(
		newServeCmd(deps),
		newStatusCmd(v, deps),
		newDoctorCmd(v, deps),
		newSessionCmd(v, deps),
		newTurnCmd(v, deps),
		newEventsCmd(v, deps),
		newLogsCmd(v, deps),
		newMetricsCmd(v, deps),
		newConfigCmd(v, deps),
		newProfileCmd(v, deps),
		newCharacterCmd(v, deps),
		newIdentityCmd(v, deps),
		newDBCmd(v, deps),
		newCompletionCmd(root),
	)
	return root
}

func normalizeDependencies(deps Dependencies) Dependencies {
	defaults := DefaultDependencies()
	if deps.Getenv == nil {
		deps.Getenv = defaults.Getenv
	}
	if deps.Stdin == nil {
		deps.Stdin = defaults.Stdin
	}
	if deps.ClientFactory == nil {
		deps.ClientFactory = defaults.ClientFactory
	}
	if deps.Serve == nil {
		deps.Serve = defaults.Serve
	}
	if deps.Database == nil {
		deps.Database = localDatabaseOperations{getenv: deps.Getenv}
	}
	return deps
}

func connectionConfig(v *viper.Viper, deps Dependencies) (ConnectionConfig, error) {
	config := ConnectionConfig{
		Endpoint: v.GetString("endpoint"),
		Timeout:  v.GetDuration("timeout"),
		Output:   v.GetString("output"),
		Token:    deps.Getenv("FAIRY_API_TOKEN"),
	}
	if config.Output != "json" && config.Output != "table" {
		return ConnectionConfig{}, errors.New("output must be json or table")
	}
	if config.Timeout <= 0 {
		return ConnectionConfig{}, errors.New("timeout must be greater than zero")
	}
	if config.Token != strings.TrimSpace(config.Token) {
		return ConnectionConfig{}, errors.New("API token must not contain leading or trailing whitespace")
	}
	return config, nil
}

func newClient(v *viper.Viper, deps Dependencies) (APIClient, ConnectionConfig, error) {
	config, err := connectionConfig(v, deps)
	if err != nil {
		return nil, ConnectionConfig{}, err
	}
	client, err := deps.ClientFactory(config)
	return client, config, err
}
