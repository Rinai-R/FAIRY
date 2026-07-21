package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"fairy-surfaces/qq-onebot/bridge"
	"fairy-surfaces/qq-onebot/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := cmd.Execute(ctx, os.Args[1:], cmd.Dependencies{Doctor: bridge.Doctor, Serve: bridge.Serve}, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
