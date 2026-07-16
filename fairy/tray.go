package main

import (
	"log"

	"fairy/desktop"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func setupSystemTray(app *application.App, service *desktop.DesktopService) {
	tray := app.SystemTray.New()
	// macOS menu bar requires a template (black + alpha) icon; a full-color
	// 512px app icon is effectively invisible on the status bar.
	tray.SetTemplateIcon(trayTemplateIcon)
	tray.SetTooltip("FAIRY")

	menu := app.Menu.New()
	menu.Add("显示").OnClick(func(ctx *application.Context) {
		if _, err := service.ShowCompanion(); err != nil {
			log.Printf("show companion from tray: %v", err)
		}
	})
	menu.Add("隐藏").OnClick(func(ctx *application.Context) {
		if _, err := service.HideCompanion(); err != nil {
			log.Printf("hide companion from tray: %v", err)
		}
	})
	menu.Add("恢复交互").OnClick(func(ctx *application.Context) {
		if _, err := service.RestoreCompanionInteraction(); err != nil {
			log.Printf("restore companion interaction from tray: %v", err)
		}
	})
	menu.Add("控制面板").OnClick(func(ctx *application.Context) {
		if _, err := service.ShowControlPanel(); err != nil {
			log.Printf("show control panel from tray: %v", err)
		}
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(ctx *application.Context) {
		app.Quit()
	})
	tray.SetMenu(menu)
	tray.OnClick(func() {
		if _, err := service.ShowCompanion(); err != nil {
			log.Printf("show companion from tray click: %v", err)
		}
	})
	if _, err := service.MarkTrayReady(); err != nil {
		log.Printf("mark tray ready: %v", err)
	}
}
