package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{Use: "fairy-qq-onebot", SilenceUsage: true, SilenceErrors: true}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(&cobra.Command{Use: "healthcheck", Short: "验证 QQ Surface 运行依赖", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := configFromEnv()
		if err != nil {
			return fmt.Errorf("config check: %w", err)
		}
		return runReadinessCheck(cmd.Context(), cfg)
	}})
	root.AddCommand(&cobra.Command{Use: "serve", Short: "运行 ZeroBot QQ Surface", Long: `运行 ZeroBot QQ Surface。

配置仅来自 FAIRY_CORE_ENDPOINT、FAIRY_CORE_TOKEN、FAIRY_ONEBOT_WEBHOOK_ENDPOINT、
	FAIRY_ONEBOT_API_ENDPOINT、FAIRY_ONEBOT_TOKEN 和 FAIRY_ONEBOT_CONTAINER_NETWORK。`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := configFromEnv()
		if err != nil {
			return fmt.Errorf("config check: %w", err)
		}
		return runBot(cmd.Context(), cfg)
	}})
	var smokeMessageID string
	var smokeWait time.Duration
	smoke := &cobra.Command{Use: "smoke", Short: "核验一条真实 QQ 入站到出站的投递证据", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if smokeWait < minimumSmokeWait || smokeWait > maximumSmokeWait {
			return errSmokeConfigInvalid
		}
		cfg, err := configFromEnv()
		if err != nil {
			return errSmokeConfigInvalid
		}
		return runDeliverySmoke(cmd.Context(), cfg, smokeMessageID, smokeWait, cmd.OutOrStdout())
	}}
	smoke.Flags().StringVar(&smokeMessageID, "message-id", "", "真实入站 OneBot message ID")
	smoke.Flags().DurationVar(&smokeWait, "wait", defaultSmokeWait, "有界等待时间（1s 到 5m）")
	_ = smoke.MarkFlagRequired("message-id")
	root.AddCommand(smoke)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root.SetContext(ctx)
	root.SetArgs(os.Args[1:])
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
