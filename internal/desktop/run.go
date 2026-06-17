package desktop

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

func Run(config bootstrap.Config, logger *slog.Logger) error {
	app := New(config, logger)

	return wails.Run(&options.App{
		Title:            "FAIRY",
		Width:            1280,
		Height:           820,
		BackgroundColour: options.NewRGBA(232, 242, 255, 255),
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
		},
		AssetServer: &assetserver.Options{
			Assets: os.DirFS(assetsDir()),
		},
		OnStartup: app.Startup,
		Bind: []interface{}{
			app,
		},
	})
}

func assetsDir() string {
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
