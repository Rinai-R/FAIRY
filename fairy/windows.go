package main

import (
	"log"

	"fairy/desktop"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type companionWindowAdapter struct {
	window application.Window
}

func (a companionWindowAdapter) Show() {
	a.window.Show()
}

func (a companionWindowAdapter) Hide() {
	a.window.Hide()
}

func (a companionWindowAdapter) SetAlwaysOnTop(enabled bool) {
	a.window.SetAlwaysOnTop(enabled)
}

func (a companionWindowAdapter) SetIgnoreMouseEvents(ignore bool) {
	a.window.SetIgnoreMouseEvents(ignore)
}

func (a companionWindowAdapter) Bounds() desktop.WindowBounds {
	x, y := a.window.Position()
	width, height := a.window.Size()
	return desktop.WindowBounds{X: x, Y: y, Width: width, Height: height}
}

func (a companionWindowAdapter) SetBounds(bounds desktop.WindowBounds) {
	// Wails SetSize animates the macOS frame with a fixed top-left, which makes
	// the bottom-right pet jump when opening/closing chat. Apply bounds atomically.
	setWindowBoundsAtomic(a.window, bounds)
}

type controlPanelWindowAdapter struct {
	window application.Window
}

func (a controlPanelWindowAdapter) Show() {
	a.window.Show()
}

func (a controlPanelWindowAdapter) Hide() {
	a.window.Hide()
}

func (a controlPanelWindowAdapter) Focus() {
	a.window.Focus()
}

func (a controlPanelWindowAdapter) SetBounds(bounds desktop.WindowBounds) {
	setWindowBoundsAtomic(a.window, bounds)
}

func attachProductWindows(app *application.App, desktopService *desktop.DesktopService) {
	companionWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "FAIRY",
		Name:             "companion",
		Width:            220,
		Height:           344,
		URL:              "/?surface=companion",
		AlwaysOnTop:      true,
		DisableResize:    true,
		Frameless:        true,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: application.MacWindow{
			Backdrop:      application.MacBackdropTransparent,
			DisableShadow: true,
			WindowLevel:   application.MacWindowLevelFloating,
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces |
				application.MacWindowCollectionBehaviorFullScreenAuxiliary |
				application.MacWindowCollectionBehaviorTransient,
			TabbingMode: application.MacWindowTabbingModeDisallowed,
		},
	})
	controlPanelWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "FAIRY 控制面板",
		Name:             "control-panel",
		Width:            560,
		Height:           620,
		URL:              "/?surface=control-panel",
		Hidden:           true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		Frameless:        true,
		EnableFileDrop:   true,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: application.MacWindow{
			Backdrop:    application.MacBackdropTransparent,
			WindowLevel: application.MacWindowLevelFloating,
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces |
				application.MacWindowCollectionBehaviorFullScreenAuxiliary |
				application.MacWindowCollectionBehaviorTransient,
			TabbingMode: application.MacWindowTabbingModeDisallowed,
		},
	})

	desktop.AttachCompanionWindow(desktopService, companionWindowAdapter{window: companionWindow})
	desktop.AttachControlPanelWindow(desktopService, controlPanelWindowAdapter{window: controlPanelWindow})
	companionWindow.Show()
	companionWindow.SetAlwaysOnTop(true)
	log.Printf("companion window attached and shown")

	controlPanelWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		if _, err := desktopService.RestoreCompanionAfterControlPanel(); err != nil {
			log.Printf("restore companion after control panel close: %v", err)
		}
	})
	controlPanelWindow.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		files := event.Context().DroppedFiles()
		if len(files) == 0 {
			return
		}
		app.Event.Emit("character-package-dropped", map[string]any{
			"files": files,
		})
	})
}
