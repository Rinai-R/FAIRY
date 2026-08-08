package model

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidatePromptItemsAcceptsTypedDesktopImageForVisionModel(t *testing.T) {
	conn := connection(ProtocolChatCompletions)
	conn.Capabilities.VisionInput = true
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png fixture"))
	items := []PromptItem{{
		Type:       PromptItemToolResult,
		ToolCallID: "call-1",
		Parts: promptContentParts(
			PromptContentPart{Type: PromptContentText, Text: "Current desktop screenshot."},
			PromptContentPart{Type: PromptContentImage, ImageDataURL: dataURL, ImageMIME: "image/png", ImagePurpose: "desktop_observation"},
		),
	}}
	if err := validatePromptItems(conn, items); err != nil {
		t.Fatalf("validatePromptItems() error = %v", err)
	}
}

func TestValidatePromptItemsRejectsUnsafeImageWithoutLeakingContent(t *testing.T) {
	fixture := "unique-private-screen-content"
	validDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(fixture))
	tests := []struct {
		name   string
		conn   Connection
		part   PromptContentPart
		needle string
	}{
		{
			name: "capability disabled", conn: connection(ProtocolChatCompletions),
			part:   PromptContentPart{Type: PromptContentImage, ImageDataURL: validDataURL, ImageMIME: "image/png", ImagePurpose: "desktop_observation"},
			needle: "does not support",
		},
		{
			name: "MIME mismatch", conn: visionConnection(),
			part:   PromptContentPart{Type: PromptContentImage, ImageDataURL: validDataURL, ImageMIME: "image/jpeg", ImagePurpose: "desktop_observation"},
			needle: "does not match",
		},
		{
			name: "oversized", conn: visionConnection(),
			part:   PromptContentPart{Type: PromptContentImage, ImageDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxPromptImageBytes+1)), ImageMIME: "image/png", ImagePurpose: "desktop_observation"},
			needle: "byte limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePromptItems(test.conn, []PromptItem{{Type: PromptItemToolResult, ToolCallID: "call-1", Parts: promptContentParts(test.part)}})
			if err == nil || !strings.Contains(err.Error(), test.needle) {
				t.Fatalf("error = %v, want containing %q", err, test.needle)
			}
			if strings.Contains(err.Error(), fixture) || strings.Contains(err.Error(), test.part.ImageDataURL) {
				t.Fatalf("error leaked image content: %v", err)
			}
		})
	}
}

func promptContentParts(parts ...PromptContentPart) *PromptContentParts {
	value := PromptContentParts(parts)
	return &value
}

func visionConnection() Connection {
	conn := connection(ProtocolChatCompletions)
	conn.Capabilities.VisionInput = true
	return conn
}
