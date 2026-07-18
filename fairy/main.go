// Command fairy starts the FAIRY Wails desktop application.
package main

import (
	"os"

	fairyapp "fairy/app"
	"fairy/logx"
	"go.uber.org/zap"
)

func main() {
	logger := logx.New()
	defer func() { _ = logger.Sync() }()

	if err := fairyapp.Run(fairyapp.Options{
		EmbeddedAssets:   embeddedAssets,
		AssetsDir:        "assets/dist",
		AppIcon:          appIcon,
		TrayTemplateIcon: trayTemplateIcon,
		Logger:           logger,
	}); err != nil {
		logger.Error("app run", zap.Error(err))
		_ = logger.Sync()
		os.Exit(1)
	}
}
