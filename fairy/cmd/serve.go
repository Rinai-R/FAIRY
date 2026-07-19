package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fairy/api"
	fairyruntime "fairy/runtime"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var addr string
	var token string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Hertz HTTP/SSE Core API (main Surface entrypoint)",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := configRootFlag(cmd)
			if err != nil {
				return err
			}
			if token == "" {
				token = os.Getenv("FAIRY_API_TOKEN")
			}
			rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: root})
			if err != nil {
				return err
			}
			defer rt.Close()

			srv, err := api.NewServer(rt, api.Options{Addr: addr, Token: token, Logger: rt.Logger})
			if err != nil {
				return err
			}
			errCh := make(chan error, 1)
			go func() {
				rt.Logger.Info(fmt.Sprintf("fairy core listening on http://%s", addr))
				errCh <- srv.Run()
			}()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			select {
			case sig := <-sigCh:
				rt.Logger.Info(fmt.Sprintf("shutting down (%s)", sig))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
				return nil
			case err := <-errCh:
				return err
			}
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8787", "listen address")
	cmd.Flags().StringVar(&token, "token", "", "optional Bearer token (or FAIRY_API_TOKEN)")
	return cmd
}
