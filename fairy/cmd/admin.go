package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const maxCLIPayload = 1 << 20

func newConfigCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Manage whitelisted Core settings", GroupID: "admin"}
	get := &cobra.Command{
		Use: "get <section>", Args: cobra.ExactArgs(1), Short: "Read a settings section",
		RunE: func(command *cobra.Command, args []string) error {
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			result, err := client.GetConfig(command.Context(), args[0])
			if err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), config.Output, result)
		},
	}
	var applyFile string
	apply := &cobra.Command{
		Use: "apply <section>", Args: cobra.ExactArgs(1), Short: "Apply a JSON settings section",
		RunE: func(command *cobra.Command, args []string) error {
			payload, err := readPayload(applyFile, deps.Stdin)
			if err != nil {
				return err
			}
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			result, err := client.ApplyConfig(command.Context(), args[0], payload)
			if err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), config.Output, result)
		},
	}
	apply.Flags().StringVar(&applyFile, "file", "", "JSON file path, or - for stdin")
	_ = apply.MarkFlagRequired("file")
	deleteCommand := &cobra.Command{
		Use: "delete <section>", Args: cobra.ExactArgs(1), Short: "Delete model or speech settings",
		RunE: func(command *cobra.Command, args []string) error {
			if args[0] != "model" && args[0] != "speech" {
				return errors.New("delete is supported only for model and speech")
			}
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			result, err := client.DeleteConfig(command.Context(), args[0])
			if err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), config.Output, result)
		},
	}
	command.AddCommand(get, apply, deleteCommand)
	return command
}

func newProfileCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "profile", Short: "Manage the user profile", GroupID: "admin"}
	get := &cobra.Command{Use: "get", Args: cobra.NoArgs, RunE: func(command *cobra.Command, args []string) error {
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		result, err := client.GetProfile(command.Context())
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, result)
	}}
	var file string
	apply := &cobra.Command{Use: "apply", Args: cobra.NoArgs, RunE: func(command *cobra.Command, args []string) error {
		payload, err := readPayload(file, deps.Stdin)
		if err != nil {
			return err
		}
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		result, err := client.ApplyProfile(command.Context(), payload)
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, result)
	}}
	apply.Flags().StringVar(&file, "file", "", "JSON file path, or - for stdin")
	_ = apply.MarkFlagRequired("file")
	deleteCommand := &cobra.Command{Use: "delete", Args: cobra.NoArgs, RunE: func(command *cobra.Command, args []string) error {
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		result, err := client.DeleteProfile(command.Context())
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, result)
	}}
	command.AddCommand(get, apply, deleteCommand)
	return command
}

func newCharacterCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "character", Short: "Manage characters", GroupID: "admin"}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(command *cobra.Command, args []string) error {
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		result, err := client.ListCharacters(command.Context())
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, result)
	}}
	var file string
	create := &cobra.Command{Use: "create", Args: cobra.NoArgs, RunE: func(command *cobra.Command, args []string) error {
		payload, err := readPayload(file, deps.Stdin)
		if err != nil {
			return err
		}
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		result, err := client.CreateCharacter(command.Context(), payload)
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, result)
	}}
	create.Flags().StringVar(&file, "file", "", "JSON file path, or - for stdin")
	_ = create.MarkFlagRequired("file")
	var characterID string
	var revision uint64
	activate := &cobra.Command{Use: "activate", Args: cobra.NoArgs, RunE: func(command *cobra.Command, args []string) error {
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		result, err := client.ActivateCharacter(command.Context(), characterID, revision)
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, result)
	}}
	activate.Flags().StringVar(&characterID, "character", "", "character ID")
	activate.Flags().Uint64Var(&revision, "revision", 0, "character revision")
	_ = activate.MarkFlagRequired("character")
	_ = activate.MarkFlagRequired("revision")
	command.AddCommand(list, create, activate)
	return command
}

func readPayload(path string, stdin io.Reader) ([]byte, error) {
	var reader io.Reader
	var closeReader io.Closer
	if path == "-" {
		reader = stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open payload: %w", err)
		}
		reader = file
		closeReader = file
	}
	if closeReader != nil {
		defer closeReader.Close()
	}
	payload, err := io.ReadAll(io.LimitReader(reader, maxCLIPayload+1))
	if err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	if len(payload) > maxCLIPayload {
		return nil, fmt.Errorf("payload exceeds %s bytes", strconv.Itoa(maxCLIPayload))
	}
	return payload, nil
}
