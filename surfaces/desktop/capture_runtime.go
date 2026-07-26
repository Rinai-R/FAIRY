package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"time"

	"fairy/contracts/observation"
	"fairy/coreclient"
	"golang.org/x/image/draw"
)

type desktopImageCapturer interface {
	CaptureMainDisplay(context.Context) (image.Image, error)
}

type desktopCaptureRuntime struct {
	capturer desktopImageCapturer
	privacy func() observation.DesktopPrivacyState
}

func newDesktopCaptureRuntime(capturer desktopImageCapturer, privacy func() observation.DesktopPrivacyState) (*desktopCaptureRuntime, error) {
	if capturer == nil || privacy == nil {
		return nil, errors.New("desktop capture runtime dependencies are required")
	}
	return &desktopCaptureRuntime{capturer: capturer, privacy: privacy}, nil
}

func (runtime *desktopCaptureRuntime) Handle(ctx context.Context, request coreclient.DesktopCaptureRequest) coreclient.DesktopCaptureResult {
	if err := request.Validate(); err != nil {
		return failedCaptureResult("invalid_request")
	}
	if ctx == nil || request.DeadlineUnixMS <= time.Now().UnixMilli() {
		return failedCaptureResult("deadline_exceeded")
	}
	if runtime == nil || runtime.capturer == nil || runtime.privacy == nil {
		return failedCaptureResult("capture_unavailable")
	}
	if runtime.privacy() != observation.DesktopPrivacyNormal {
		return failedCaptureResult("privacy_blocked")
	}
	select {
	case <-ctx.Done():
		return failedCaptureResult("deadline_exceeded")
	default:
	}
	frame, err := runtime.capturer.CaptureMainDisplay(ctx)
	if err != nil {
		return failedCaptureResult(captureErrorCode(err))
	}
	result, err := encodeDesktopCapture(frame, request)
	if err != nil {
		return failedCaptureResult("encoding_failed")
	}
	return result
}

func failedCaptureResult(code string) coreclient.DesktopCaptureResult {
	return coreclient.DesktopCaptureResult{Status: "failed", ErrorCode: code, ErrorMessage: "desktop capture failed"}
}

func captureErrorCode(err error) string {
	switch {
	case errors.Is(err, errScreenRecordingPermissionDenied):
		return "permission_denied"
	case errors.Is(err, errDesktopCaptureUnsupported):
		return "unsupported_platform"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "deadline_exceeded"
	default:
		return "capture_failed"
	}
}

func encodeDesktopCapture(source image.Image, request coreclient.DesktopCaptureRequest) (coreclient.DesktopCaptureResult, error) {
	if source == nil || source.Bounds().Dx() <= 0 || source.Bounds().Dy() <= 0 {
		return coreclient.DesktopCaptureResult{}, errors.New("captured image is empty")
	}
	maxDimension := request.MaxDimension
	if maxDimension <= 0 {
		return coreclient.DesktopCaptureResult{}, errors.New("capture dimension limit is invalid")
	}
	width, height := boundedCaptureDimensions(source.Bounds().Dx(), source.Bounds().Dy(), maxDimension)
	current := resizeCapture(source, width, height)
	allowed := make(map[string]bool, len(request.AllowedMIMETypes))
	for _, mediaType := range request.AllowedMIMETypes {
		allowed[mediaType] = true
	}

	for attempt := 0; attempt < 8; attempt++ {
		if allowed["image/png"] {
			if result, ok := encodeCapturePNG(current, request.MaxDecodedBytes); ok {
				return result, nil
			}
		}
		if allowed["image/jpeg"] {
			for _, quality := range []int{88, 78, 68, 58, 48} {
				if result, ok := encodeCaptureJPEG(current, quality, request.MaxDecodedBytes); ok {
					return result, nil
				}
			}
		}
		bounds := current.Bounds()
		if bounds.Dx() <= 320 || bounds.Dy() <= 180 {
			break
		}
		current = resizeCapture(current, max(1, bounds.Dx()*3/4), max(1, bounds.Dy()*3/4))
	}
	return coreclient.DesktopCaptureResult{}, errors.New("capture cannot satisfy encoding budget")
}

func boundedCaptureDimensions(width, height, maxDimension int) (int, int) {
	scale := math.Min(1, math.Min(float64(maxDimension)/float64(width), float64(maxDimension)/float64(height)))
	if pixels := float64(width) * float64(height) * scale * scale; pixels > 16_000_000 {
		scale *= math.Sqrt(16_000_000 / pixels)
	}
	return max(1, int(math.Floor(float64(width)*scale))), max(1, int(math.Floor(float64(height)*scale)))
}

func resizeCapture(source image.Image, width, height int) *image.RGBA {
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(destination, destination.Bounds(), source, source.Bounds(), draw.Over, nil)
	return destination
}

func encodeCapturePNG(frame image.Image, limit int) (coreclient.DesktopCaptureResult, bool) {
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&output, frame); err != nil || output.Len() > limit {
		return coreclient.DesktopCaptureResult{}, false
	}
	return completedCaptureResult("image/png", frame.Bounds().Dx(), frame.Bounds().Dy(), output.Bytes()), true
}

func encodeCaptureJPEG(frame image.Image, quality, limit int) (coreclient.DesktopCaptureResult, bool) {
	var output bytes.Buffer
	if err := jpeg.Encode(&output, frame, &jpeg.Options{Quality: quality}); err != nil || output.Len() > limit {
		return coreclient.DesktopCaptureResult{}, false
	}
	return completedCaptureResult("image/jpeg", frame.Bounds().Dx(), frame.Bounds().Dy(), output.Bytes()), true
}

func completedCaptureResult(mediaType string, width, height int, content []byte) coreclient.DesktopCaptureResult {
	digest := sha256.Sum256(content)
	return coreclient.DesktopCaptureResult{
		Status: "completed", MediaType: mediaType, Width: width, Height: height, ByteCount: len(content),
		SHA256: hex.EncodeToString(digest[:]), DataURL: "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(content),
	}
}
