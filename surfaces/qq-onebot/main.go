package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{Use: "fairy-qq-onebot", SilenceUsage: true, SilenceErrors: true}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(&cobra.Command{Use: "serve", Short: "运行 ZeroBot QQ Surface", Long: `运行 ZeroBot QQ Surface。

配置仅来自 FAIRY_CORE_ENDPOINT、FAIRY_CORE_TOKEN、FAIRY_ONEBOT_WEBHOOK_ENDPOINT、
FAIRY_ONEBOT_API_ENDPOINT、FAIRY_ONEBOT_TOKEN 和 FAIRY_ONEBOT_GROUP_ALLOWLIST。`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := configFromEnv()
		if err != nil {
			return fmt.Errorf("config check: %w", err)
		}
		return runBot(cmd.Context(), cfg)
	}})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root.SetContext(ctx)
	root.SetArgs(os.Args[1:])
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func splitAllowlist(raw string) []string {
	parts := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) != "" {
			parts = append(parts, strings.TrimSpace(part))
		}
	}
	return parts
}
