//go:build !darwin

package main

import (
	"context"
	"errors"
	"image"
)

var (
	errScreenRecordingPermissionDenied = errors.New("screen recording permission denied")
	errDesktopCaptureUnsupported       = errors.New("desktop capture is unsupported")
)

type platformDesktopCapturer struct{}

func newPlatformDesktopCapturer() desktopImageCapturer { return platformDesktopCapturer{} }

func (platformDesktopCapturer) CaptureMainDisplay(context.Context) (image.Image, error) {
	return nil, errDesktopCaptureUnsupported
}
