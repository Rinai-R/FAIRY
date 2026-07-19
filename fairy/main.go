// Command fairy runs the FAIRY Session Core HTTP/SSE server.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"fairy/api"
	fairyruntime "fairy/runtime"
)

func main() {
	configRoot := flag.String("config-root", "", "config root (default: FAIRY_CONFIG_ROOT or platform path)")
	addr := flag.String("addr", "", "listen address (default: FAIRY_LISTEN_ADDR or 127.0.0.1:8787)")
	token := flag.String("token", "", "optional Bearer token (or FAIRY_API_TOKEN)")
	flag.Parse()

	if err := run(*configRoot, *addr, *token); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configRoot, addr, token string) error {
	if strings.TrimSpace(token) == "" {
		token = os.Getenv("FAIRY_API_TOKEN")
	}
	listen := strings.TrimSpace(addr)
	if listen == "" {
		listen = strings.TrimSpace(os.Getenv("FAIRY_LISTEN_ADDR"))
	}
	if listen == "" {
		listen = "127.0.0.1:8787"
	}

	rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: configRoot})
	if err != nil {
		return err
	}
	defer rt.Close()

	srv, err := api.NewServer(rt, api.Options{Addr: listen, Token: token, Logger: rt.Logger})
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		rt.Logger.Info(fmt.Sprintf("fairy core listening on http://%s", listen))
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
}
