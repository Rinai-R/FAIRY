package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"testing"
	"time"

	"fairy/contracts/observation"
	"fairy/coreclient"
)

type fakeDesktopCapturer struct {
	calls int
	frame image.Image
	err   error
}

func (capturer *fakeDesktopCapturer) CaptureMainDisplay(context.Context) (image.Image, error) {
	capturer.calls++
	return capturer.frame, capturer.err
}

func TestDesktopCaptureRuntimeRejectsPrivacyAndExpiredBeforeCapture(t *testing.T) {
	capturer := &fakeDesktopCapturer{frame: image.NewRGBA(image.Rect(0, 0, 2, 2))}
	privacy := observation.DesktopPrivacyProtected
	runtime, err := newDesktopCaptureRuntime(capturer, func() observation.DesktopPrivacyState { return privacy })
	if err != nil {
		t.Fatal(err)
	}
	request := validDesktopCaptureRequest()
	result := runtime.Handle(t.Context(), request)
	if result.Status != "failed" || result.ErrorCode != "privacy_blocked" || capturer.calls != 0 {
		t.Fatalf("privacy result = %#v, calls=%d", result, capturer.calls)
	}
	privacy = observation.DesktopPrivacyNormal
	request.DeadlineUnixMS = time.Now().Add(-time.Second).UnixMilli()
	result = runtime.Handle(t.Context(), request)
	if result.Status != "failed" || result.ErrorCode != "deadline_exceeded" || capturer.calls != 0 {
		t.Fatalf("expired result = %#v, calls=%d", result, capturer.calls)
	}
}

func TestDesktopCaptureRuntimeMapsPermissionDenial(t *testing.T) {
	capturer := &fakeDesktopCapturer{err: errScreenRecordingPermissionDenied}
	runtime, err := newDesktopCaptureRuntime(capturer, func() observation.DesktopPrivacyState { return observation.DesktopPrivacyNormal })
	if err != nil {
		t.Fatal(err)
	}
	result := runtime.Handle(t.Context(), validDesktopCaptureRequest())
	if result.Status != "failed" || result.ErrorCode != "permission_denied" || result.DataURL != "" {
		t.Fatalf("permission result = %#v", result)
	}
}

func TestEncodeDesktopCaptureHonorsDimensionAndByteBudgets(t *testing.T) {
	frame := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1200; x++ {
			frame.SetRGBA(x, y, color.RGBA{R: uint8((x*31 + y*17) % 256), G: uint8((x*7 + y*29) % 256), B: uint8((x*13 + y*11) % 256), A: 255})
		}
	}
	request := validDesktopCaptureRequest()
	request.MaxDecodedBytes = 64 << 10
	request.MaxDimension = 640
	result, err := encodeDesktopCapture(frame, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Width > 640 || result.Height > 640 || result.ByteCount > request.MaxDecodedBytes {
		t.Fatalf("bounded result = %#v", result)
	}
	prefix := "data:" + result.MediaType + ";base64,"
	if len(result.DataURL) <= len(prefix) || result.DataURL[:len(prefix)] != prefix {
		t.Fatalf("data URL prefix = %q", result.DataURL[:min(len(result.DataURL), len(prefix))])
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(result.DataURL[len(prefix):])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(decoded)
	if len(decoded) != result.ByteCount || hex.EncodeToString(digest[:]) != result.SHA256 {
		t.Fatal("capture result metadata does not match encoded bytes")
	}
}

func TestDesktopCaptureRuntimeDoesNotFallbackAfterCaptureFailure(t *testing.T) {
	capturer := &fakeDesktopCapturer{err: errors.New("capture failed")}
	runtime, err := newDesktopCaptureRuntime(capturer, func() observation.DesktopPrivacyState { return observation.DesktopPrivacyNormal })
	if err != nil {
		t.Fatal(err)
	}
	result := runtime.Handle(t.Context(), validDesktopCaptureRequest())
	if result.Status != "failed" || result.ErrorCode != "capture_failed" || result.DataURL != "" || capturer.calls != 1 {
		t.Fatalf("capture failure result = %#v, calls=%d", result, capturer.calls)
	}
}

func validDesktopCaptureRequest() coreclient.DesktopCaptureRequest {
	return coreclient.DesktopCaptureRequest{
		ExecutionID: "execution-1", ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1",
		DeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(), MaxDecodedBytes: 768 << 10,
		MaxDimension: 2048, AllowedMIMETypes: []string{"image/png", "image/jpeg"},
	}
}
