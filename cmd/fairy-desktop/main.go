package main

import (
	"log/slog"
	"os"

	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
	"github.com/Rinai-R/FAIRY/internal/desktop"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app := desktop.New(bootstrap.DefaultConfig(), logger)

	err := wails.Run(&options.App{
		Title:  "FAIRY",
		Width:  1280,
		Height: 820,
		AssetServer: &assetserver.Options{
			Assets: os.DirFS("web/dist"),
		},
		OnStartup: app.Startup,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		logger.Error("FAIRY 桌面应用退出", "error", err)
		os.Exit(1)
	}
}
