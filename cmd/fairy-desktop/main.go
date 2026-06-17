package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
	"github.com/Rinai-R/FAIRY/internal/desktop"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app := desktop.New(bootstrap.DefaultConfig(), logger)
	assetsDir := desktopAssetsDir()

	err := wails.Run(&options.App{
		Title:     "FAIRY",
		Width:     1280,
		Height:    820,
		Frameless: true,
		AssetServer: &assetserver.Options{
			Assets: os.DirFS(assetsDir),
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

func desktopAssetsDir() string {
	if _, err := os.Stat("web/dist/index.html"); err == nil {
		return "web/dist"
	}
	executable, err := os.Executable()
	if err != nil {
		return "web/dist"
	}
	bundleAssets := filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "Resources", "web", "dist"))
	if _, err := os.Stat(filepath.Join(bundleAssets, "index.html")); err == nil {
		return bundleAssets
	}
	return "web/dist"
}
