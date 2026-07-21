package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"fairy-surfaces/qq-onebot/bridge"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	envCoreEndpoint   = "FAIRY_CORE_ENDPOINT"
	envCoreToken      = "FAIRY_CORE_TOKEN"
	envOneBotEndpoint = "FAIRY_ONEBOT_ENDPOINT"
	envOneBotToken    = "FAIRY_ONEBOT_TOKEN"
	envOneBotSelfID   = "FAIRY_ONEBOT_SELF_ID"
	envOneBotGroups   = "FAIRY_ONEBOT_GROUP_ALLOWLIST"
)

type Dependencies struct {
	Doctor func(context.Context, bridge.Config) error
	Serve  func(context.Context, bridge.Config) error
}

func NewRoot(deps Dependencies, output io.Writer) *cobra.Command {
	if output == nil {
		output = io.Discard
	}
	root := &cobra.Command{Use: "fairy-qq-onebot", Short: "FAIRY OneBot 11 group bridge", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(output)
	root.AddCommand(&cobra.Command{Use: "doctor", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := ConfigFromEnv()
		if err != nil {
			return fmt.Errorf("config check: %w", err)
		}
		if deps.Doctor != nil {
			if err := deps.Doctor(cmd.Context(), cfg); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "doctor: ok")
		return nil
	}})
	root.AddCommand(&cobra.Command{Use: "serve", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := ConfigFromEnv()
		if err != nil {
			return err
		}
		if deps.Serve == nil {
			return errors.New("serve dependency is not configured")
		}
		return deps.Serve(cmd.Context(), cfg)
	}})
	return root
}

func ConfigFromEnv() (bridge.Config, error) {
	v := viper.New()
	for _, key := range []string{envCoreEndpoint, envCoreToken, envOneBotEndpoint, envOneBotToken, envOneBotSelfID, envOneBotGroups} {
		_ = v.BindEnv(key)
	}
	groups := splitAllowlist(v.GetString(envOneBotGroups))
	cfg := bridge.Config{CoreEndpoint: v.GetString(envCoreEndpoint), CoreToken: v.GetString(envCoreToken), OneBotEndpoint: v.GetString(envOneBotEndpoint), OneBotToken: v.GetString(envOneBotToken), SelfID: v.GetString(envOneBotSelfID), GroupAllowlist: groups}
	if err := cfg.Validate(); err != nil {
		return bridge.Config{}, err
	}
	return cfg, nil
}

func splitAllowlist(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, strings.TrimSpace(part))
		}
	}
	return out
}

func Execute(ctx context.Context, args []string, deps Dependencies, output io.Writer) error {
	root := NewRoot(deps, output)
	root.SetArgs(args)
	root.SetContext(ctx)
	return root.Execute()
}

func Main() {
	if err := Execute(context.Background(), os.Args[1:], Dependencies{}, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
