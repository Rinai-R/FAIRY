// Package core owns the Session Core server lifecycle independently of Cobra.
package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fairy/api"
	fairyruntime "fairy/runtime"
)

const shutdownTimeout = 5 * time.Second

type Options struct {
	ConfigRoot string
	Addr       string
	Token      string
}

type managedServer interface {
	Run() error
	Shutdown(context.Context) error
}

func Run(ctx context.Context, options Options) error {
	rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: options.ConfigRoot})
	if err != nil {
		return err
	}
	defer rt.Close()

	srv, err := api.NewServer(rt, api.Options{Addr: options.Addr, Token: options.Token, Logger: rt.Logger})
	if err != nil {
		return err
	}
	rt.Logger.Info(fmt.Sprintf("fairy core listening on http://%s", options.Addr))
	return runManaged(ctx, srv)
}

func runManaged(ctx context.Context, srv managedServer) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown core: %w", err)
		}
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		default:
		}
		return nil
	case err := <-errCh:
		return err
	}
}
