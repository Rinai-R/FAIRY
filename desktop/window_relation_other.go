//go:build !darwin || !cgo

package main

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type unsupportedWindowRelation struct{}

func newPlatformWindowRelation() windowRelation { return unsupportedWindowRelation{} }

func (unsupportedWindowRelation) Attach(application.Window, application.Window) error {
	return errors.New("native settings-window follow is unsupported on this platform")
}

func (unsupportedWindowRelation) Detach(application.Window, application.Window) error { return nil }
