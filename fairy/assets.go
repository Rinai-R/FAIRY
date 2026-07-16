package main

import "embed"

// assets/ is populated by Taskfile sync:assets (go:embed cannot use ..).
//
//go:embed all:assets/dist
var embeddedAssets embed.FS

//go:embed assets/icon.png
var appIcon []byte

//go:embed assets/tray-template.png
var trayTemplateIcon []byte
