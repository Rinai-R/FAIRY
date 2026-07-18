package main

import (
	"fairy/desktop"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"go.uber.org/zap"
)

// macCurrentSpaceCollectionBehavior keeps FAIRY product windows attached to
// the macOS Space where they are opened. Do not add CanJoinAllSpaces here:
// users expect the pet, control panel, and speech bubble to stay on one Space
// instead of following every trackpad desktop swipe.
const macCurrentSpaceCollectionBehavior = application.MacWindowCollectionBehaviorManaged |
	application.MacWindowCollectionBehaviorFullScreenNone |
	application.MacWindowCollectionBehaviorTransient

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

type speechBubbleWindowAdapter struct {
	window application.Window
}

func (a speechBubbleWindowAdapter) Show() {
	a.window.Show()
}

func (a speechBubbleWindowAdapter) Hide() {
	a.window.Hide()
}

func (a speechBubbleWindowAdapter) SetBounds(bounds desktop.WindowBounds) {
	setWindowBoundsAtomic(a.window, bounds)
}

func attachProductWindows(app *application.App, desktopService *desktop.DesktopService, logger *zap.Logger) {
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
			Backdrop:           application.MacBackdropTransparent,
			DisableShadow:      true,
			WindowLevel:        application.MacWindowLevelFloating,
			CollectionBehavior: macCurrentSpaceCollectionBehavior,
			TabbingMode:        application.MacWindowTabbingModeDisallowed,
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
			Backdrop:           application.MacBackdropTransparent,
			WindowLevel:        application.MacWindowLevelFloating,
			CollectionBehavior: macCurrentSpaceCollectionBehavior,
			TabbingMode:        application.MacWindowTabbingModeDisallowed,
		},
	})

	speechBubbleWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "FAIRY 气泡",
		Name:             "speech-bubble",
		Width:            250,
		Height:           200,
		URL:              "/?surface=speech",
		Hidden:           true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		Frameless:        true,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: application.MacWindow{
			Backdrop:           application.MacBackdropTransparent,
			DisableShadow:      true,
			WindowLevel:        application.MacWindowLevelFloating,
			CollectionBehavior: macCurrentSpaceCollectionBehavior,
			TabbingMode:        application.MacWindowTabbingModeDisallowed,
		},
	})
	// The bubble never receives input; it only floats above the character.
	speechBubbleWindow.SetIgnoreMouseEvents(true)
	speechBubbleWindow.SetAlwaysOnTop(true)

	desktop.AttachCompanionWindow(desktopService, companionWindowAdapter{window: companionWindow})
	desktop.AttachControlPanelWindow(desktopService, controlPanelWindowAdapter{window: controlPanelWindow})
	desktop.AttachSpeechBubbleWindow(desktopService, speechBubbleWindowAdapter{window: speechBubbleWindow})
	companionWindow.Show()
	companionWindow.SetAlwaysOnTop(true)
	logger.Info("companion window attached and shown")

	// Keep the bubble window glued to the character while it is dragged.
	companionWindow.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) {
		desktopService.RepositionSpeechBubble()
	})

	controlPanelWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		if _, err := desktopService.RestoreCompanionAfterControlPanel(); err != nil {
			logger.Error("restore companion after control panel close", zap.Error(err))
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
