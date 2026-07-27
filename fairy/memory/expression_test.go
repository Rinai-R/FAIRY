package memory

import (
	"strings"
	"testing"
)

func TestValidateExpressionMessageAndTextProjection(t *testing.T) {
	parts := []ExpressionPart{
		{Kind: ExpressionUtterance, Text: "前一句。", VisualState: "idle"},
		{Kind: ExpressionSticker, VisualState: "surprised", Sticker: &StickerSnapshot{
			ID: "sticker-1", Description: "震惊和无语", MIMEType: "image/webp",
		}},
		{Kind: ExpressionUtterance, Text: "后一句。", VisualState: "happy"},
	}
	if got := ExpressionTextProjection(parts); got != "前一句。\n后一句。" {
		t.Fatalf("text projection = %q", got)
	}
	if err := ValidateExpressionMessage("前一句。\n后一句。", parts); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExpressionMessage("", []ExpressionPart{parts[1]}); err != nil {
		t.Fatalf("pure sticker expression = %v", err)
	}
}

func TestValidateExpressionMessageRejectsInvalidShapes(t *testing.T) {
	sticker := ExpressionPart{Kind: ExpressionSticker, VisualState: "idle", Sticker: &StickerSnapshot{
		ID: "sticker-1", Description: "震惊", MIMEType: "image/png",
	}}
	tests := []struct {
		name    string
		content string
		parts   []ExpressionPart
	}{
		{name: "legacy empty", parts: nil},
		{name: "projection mismatch", content: "别的文本", parts: []ExpressionPart{{Kind: ExpressionUtterance, Text: "实际文本", VisualState: "idle"}}},
		{name: "two stickers", parts: []ExpressionPart{sticker, sticker}},
		{name: "sticker text", content: "marker", parts: []ExpressionPart{{Kind: ExpressionSticker, Text: "marker", VisualState: "idle", Sticker: sticker.Sticker}}},
		{name: "unknown kind", parts: []ExpressionPart{{Kind: "other", VisualState: "idle"}}},
		{name: "missing snapshot", parts: []ExpressionPart{{Kind: ExpressionSticker, VisualState: "idle"}}},
		{name: "unsupported MIME", parts: []ExpressionPart{{Kind: ExpressionSticker, VisualState: "idle", Sticker: &StickerSnapshot{ID: "sticker-1", Description: "震惊", MIMEType: "image/svg+xml"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateExpressionMessage(tt.content, tt.parts); err == nil {
				t.Fatal("invalid expression message accepted")
			}
		})
	}
}

func TestPromptMessageTextUsesOnlyOrderedSnapshotDescriptions(t *testing.T) {
	message := MessageRecord{
		Role: "assistant", Content: "前一句。\n后一句。",
		Parts: []ExpressionPart{
			{Kind: ExpressionUtterance, Text: "前一句。", VisualState: "idle"},
			{Kind: ExpressionSticker, VisualState: "idle", Sticker: &StickerSnapshot{
				ID: "secret-sticker-id", Description: "震惊和无语", MIMEType: "image/png",
			}},
			{Kind: ExpressionUtterance, Text: "后一句。", VisualState: "idle"},
		},
	}
	got := PromptMessageText(message)
	if got != "前一句。\n[表情包：震惊和无语]\n后一句。" {
		t.Fatalf("prompt projection = %q", got)
	}
	if strings.Contains(got, "secret-sticker-id") || strings.Contains(got, "image/png") {
		t.Fatalf("prompt projection leaked asset internals: %q", got)
	}
	legacy := MessageRecord{Role: "assistant", Content: "旧文本"}
	if got := PromptMessageText(legacy); got != "旧文本" {
		t.Fatalf("legacy projection = %q", got)
	}
}
