//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ScreenCaptureKit
#include <stdlib.h>

int fairy_preflight_screen_capture(void);
int fairy_capture_main_display(unsigned char **pixels, size_t *width, size_t *height, size_t *stride, long long timeout_ms);
*/
import "C"

import (
	"context"
	"errors"
	"image"
	"time"
	"unsafe"
)

var (
	errScreenRecordingPermissionDenied = errors.New("macOS screen recording permission denied")
	errDesktopCaptureUnsupported       = errors.New("desktop capture is unsupported")
)

type platformDesktopCapturer struct{}

func newPlatformDesktopCapturer() desktopImageCapturer { return platformDesktopCapturer{} }

func (platformDesktopCapturer) CaptureMainDisplay(ctx context.Context) (image.Image, error) {
	if ctx == nil {
		return nil, errors.New("capture context is required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if C.fairy_preflight_screen_capture() == 0 {
		return nil, errScreenRecordingPermissionDenied
	}
	timeout := 15 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	var pixels *C.uchar
	var width, height, stride C.size_t
	status := C.fairy_capture_main_display(&pixels, &width, &height, &stride, C.longlong(timeout.Milliseconds()))
	switch status {
	case 0:
	case 5:
		return nil, errDesktopCaptureUnsupported
	case 6:
		return nil, context.DeadlineExceeded
	default:
		return nil, errors.New("capturing main display failed")
	}
	defer C.free(unsafe.Pointer(pixels))
	byteCount := int(height * stride)
	content := C.GoBytes(unsafe.Pointer(pixels), C.int(byteCount))
	frame := &image.RGBA{Pix: content, Stride: int(stride), Rect: image.Rect(0, 0, int(width), int(height))}
	return frame, nil
}
