//go:build !darwin

package app

import (
	"fairy/desktop"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func setWindowBoundsAtomic(window application.Window, bounds desktop.WindowBounds) {
	if window == nil {
		return
	}
	// Prefer position-then-size so bottom-right anchored growth stays closer to stable.
	window.SetPosition(bounds.X, bounds.Y)
	window.SetSize(bounds.Width, bounds.Height)
}
