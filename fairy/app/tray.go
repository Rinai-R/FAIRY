package app

import (
	"fairy/desktop"
	"github.com/wailsapp/wails/v3/pkg/application"
	"go.uber.org/zap"
)

func setupSystemTray(app *application.App, service *desktop.DesktopService, logger *zap.Logger, trayTemplateIcon []byte) {
	tray := app.SystemTray.New()
	// macOS menu bar requires a template (black + alpha) icon; a full-color
	// 512px app icon is effectively invisible on the status bar.
	tray.SetTemplateIcon(trayTemplateIcon)
	tray.SetTooltip("FAIRY")

	menu := app.Menu.New()
	menu.Add("显示").OnClick(func(ctx *application.Context) {
		if _, err := service.ShowCompanion(); err != nil {
			logger.Error("show companion from tray", zap.Error(err))
		}
	})
	menu.Add("隐藏").OnClick(func(ctx *application.Context) {
		if _, err := service.HideCompanion(); err != nil {
			logger.Error("hide companion from tray", zap.Error(err))
		}
	})
	menu.Add("恢复交互").OnClick(func(ctx *application.Context) {
		if _, err := service.RestoreCompanionInteraction(); err != nil {
			logger.Error("restore companion interaction from tray", zap.Error(err))
		}
	})
	menu.Add("控制面板").OnClick(func(ctx *application.Context) {
		if _, err := service.ShowControlPanel(); err != nil {
			logger.Error("show control panel from tray", zap.Error(err))
		}
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(ctx *application.Context) {
		app.Quit()
	})
	tray.SetMenu(menu)
	tray.OnClick(func() {
		if _, err := service.ShowCompanion(); err != nil {
			logger.Error("show companion from tray click", zap.Error(err))
		}
	})
	if _, err := service.MarkTrayReady(); err != nil {
		logger.Error("mark tray ready", zap.Error(err))
	}
}
