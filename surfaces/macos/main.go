package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	service := NewAppService(systemTokenStore{})
	if err := wails.Run(&options.App{
		Title: "FAIRY", Width: 360, Height: 500, MinWidth: 280, MinHeight: 430,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup:   service.Startup,
		OnShutdown:  service.Shutdown,
		Bind:        []any{service},
	}); err != nil {
		log.Fatal(err)
	}
}
