// Command fairy runs the FAIRY Session Core server and debugging client.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	fairycmd "fairy/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := fairycmd.NewRootCmd(fairycmd.DefaultDependencies())
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
