package main

import (
	"log/slog"
	"os"

	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
	"github.com/Rinai-R/FAIRY/internal/desktop"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := desktop.Run(bootstrap.DefaultConfig(), logger); err != nil {
		logger.Error("FAIRY 桌面应用退出", "error", err)
		os.Exit(1)
	}
}
