package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	surfaceRevision       = "20260814-local-runtime-1"
	controlPanelWidth     = 420
	controlPanelHeight    = 560
	controlPanelMinWidth  = 360
	controlPanelMinHeight = 360
)

func main() {
	if handled, err := runPackageVerificationCommand(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	frontend, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	core := NewCoreService()
	app := application.New(application.Options{
		Name:     "FAIRY",
		Services: []application.Service{application.NewServiceWithOptions(core, application.ServiceOptions{Route: "/characters"})},
		Assets:   application.AssetOptions{Handler: application.AssetFileServerFS(frontend)},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyRegular,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})
	core.attachEmitter(func(name string, payload any) { app.Event.Emit(name, payload) })
	core.attachApplication(app)
	companion := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "FAIRY", Name: "companion", URL: "/?surface=companion&revision=" + surfaceRevision, Width: 220, Height: 344,
		DisableResize: true, Frameless: true, AlwaysOnTop: true,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac:              companionWindowOptions(),
	})
	settings := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "FAIRY 设置", Name: "control-panel", URL: "/?surface=control-panel&revision=" + surfaceRevision,
		Width: controlPanelWidth, Height: controlPanelHeight, MinWidth: controlPanelMinWidth, MinHeight: controlPanelMinHeight,
		Hidden: true, Frameless: true, AlwaysOnTop: true,
		BackgroundType: application.BackgroundTypeTransparent, BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: controlPanelWindowOptions(),
	})
	history := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "FAIRY 历史", Name: "history", URL: "/?surface=history&revision=" + surfaceRevision, Width: 332, Height: 368,
		Hidden: true, DisableResize: true, Frameless: true, AlwaysOnTop: true,
		BackgroundType: application.BackgroundTypeTransparent, BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: historyWindowOptions(),
	})
	bubble := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "FAIRY 气泡", Name: "speech-bubble", URL: "/?surface=speech&revision=" + surfaceRevision, Width: 250, Height: 200,
		Hidden: true, DisableResize: true, Frameless: true, AlwaysOnTop: true,
		BackgroundType: application.BackgroundTypeTransparent, BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: speechBubbleWindowOptions(),
	})
	management := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "FAIRY 管理工作区", Name: "management", URL: "/?surface=management&revision=" + surfaceRevision,
		Width: managementWidth, Height: managementHeight, MinWidth: managementMinWidth, MinHeight: managementMinHeight,
		Hidden: true, BackgroundType: application.BackgroundTypeSolid, BackgroundColour: application.NewRGB(244, 248, 252),
		Mac: managementWindowOptions(),
	})
	bubble.SetIgnoreMouseEvents(true)
	core.attachWindows(companion, settings, history, bubble)
	core.attachManagement(management)
	installApplicationMenu(app, core)
	companion.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) {
		core.RepositionSpeechBubble()
		core.RepositionHistory()
	})
	settings.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) {
		core.refreshControlPanelWidth()
		core.repositionControlPanel()
	})
	settings.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) { event.Cancel(); core.CloseControlPanel() })
	history.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) { event.Cancel(); core.CloseHistory() })
	management.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) { event.Cancel(); _ = core.CloseManagement() })
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// The companion itself is a transient floating window. The application remains
// a regular macOS app so it is available in the system Dock and its Dock menu.
const macCurrentSpaceCollectionBehavior = application.MacWindowCollectionBehaviorTransient |
	application.MacWindowCollectionBehaviorFullScreenNone |
	application.MacWindowCollectionBehaviorIgnoresCycle

func companionWindowOptions() application.MacWindow {
	return application.MacWindow{Backdrop: application.MacBackdropTransparent, DisableShadow: true, WindowLevel: application.MacWindowLevelFloating, CollectionBehavior: macCurrentSpaceCollectionBehavior, TabbingMode: application.MacWindowTabbingModeDisallowed}
}

func controlPanelWindowOptions() application.MacWindow {
	return application.MacWindow{Backdrop: application.MacBackdropTransparent, WindowLevel: application.MacWindowLevelFloating, CollectionBehavior: macCurrentSpaceCollectionBehavior, TabbingMode: application.MacWindowTabbingModeDisallowed}
}

func historyWindowOptions() application.MacWindow {
	return application.MacWindow{Backdrop: application.MacBackdropTransparent, WindowLevel: application.MacWindowLevelFloating, CollectionBehavior: macCurrentSpaceCollectionBehavior, TabbingMode: application.MacWindowTabbingModeDisallowed}
}

func speechBubbleWindowOptions() application.MacWindow { return companionWindowOptions() }

func managementWindowOptions() application.MacWindow {
	return application.MacWindow{TabbingMode: application.MacWindowTabbingModeDisallowed}
}
