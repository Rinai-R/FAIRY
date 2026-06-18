package desktop

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
			Assets:  os.DirFS(assetsDir()),
			Handler: runtimeAssetHandler(config),
		},
		OnStartup: app.Startup,
		Bind: []interface{}{
			app,
		},
	})
}

func runtimeAssetHandler(config bootstrap.Config) http.Handler {
	audioFiles := http.StripPrefix("/audio/", http.FileServer(http.Dir(config.AudioDir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/audio/") {
			audioFiles.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
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
