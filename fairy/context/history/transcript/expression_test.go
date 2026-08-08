package transcript

import (
	"strings"
	"testing"

	historyexpr "fairy/context/history/expression"
)

func TestValidateExpressionMessageAndTextProjection(t *testing.T) {
	parts := []historyexpr.Part{
		{Kind: historyexpr.Utterance, Text: "前一句。", VisualState: "idle"},
		{Kind: historyexpr.Sticker, VisualState: "surprised", Sticker: &historyexpr.StickerSnapshot{
			ID: "sticker-1", Description: "震惊和无语", MIMEType: "image/webp",
		}},
		{Kind: historyexpr.Utterance, Text: "后一句。", VisualState: "happy"},
	}
	if got := historyexpr.TextProjection(parts); got != "前一句。\n后一句。" {
		t.Fatalf("text projection = %q", got)
	}
	if err := validateExpressionMessage("前一句。\n后一句。", parts); err != nil {
		t.Fatal(err)
	}
	if err := validateExpressionMessage("", []historyexpr.Part{parts[1]}); err != nil {
		t.Fatalf("pure sticker expression = %v", err)
	}
}

func TestValidateExpressionMessageRejectsInvalidShapes(t *testing.T) {
	sticker := historyexpr.Part{Kind: historyexpr.Sticker, VisualState: "idle", Sticker: &historyexpr.StickerSnapshot{
		ID: "sticker-1", Description: "震惊", MIMEType: "image/png",
	}}
	tests := []struct {
		name    string
		content string
		parts   []historyexpr.Part
	}{
		{name: "legacy empty", parts: nil},
		{name: "projection mismatch", content: "别的文本", parts: []historyexpr.Part{{Kind: historyexpr.Utterance, Text: "实际文本", VisualState: "idle"}}},
		{name: "two stickers", parts: []historyexpr.Part{sticker, sticker}},
		{name: "sticker text", content: "marker", parts: []historyexpr.Part{{Kind: historyexpr.Sticker, Text: "marker", VisualState: "idle", Sticker: sticker.Sticker}}},
		{name: "unknown kind", parts: []historyexpr.Part{{Kind: "other", VisualState: "idle"}}},
		{name: "missing snapshot", parts: []historyexpr.Part{{Kind: historyexpr.Sticker, VisualState: "idle"}}},
		{name: "unsupported MIME", parts: []historyexpr.Part{{Kind: historyexpr.Sticker, VisualState: "idle", Sticker: &historyexpr.StickerSnapshot{ID: "sticker-1", Description: "震惊", MIMEType: "image/svg+xml"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateExpressionMessage(tt.content, tt.parts); err == nil {
				t.Fatal("invalid expression message accepted")
			}
		})
	}
}

func TestPromptMessageTextUsesOnlyOrderedSnapshotDescriptions(t *testing.T) {
	message := MessageRecord{
		Role: "assistant", Content: "前一句。\n后一句。",
		Parts: []historyexpr.Part{
			{Kind: historyexpr.Utterance, Text: "前一句。", VisualState: "idle"},
			{Kind: historyexpr.Sticker, VisualState: "idle", Sticker: &historyexpr.StickerSnapshot{
				ID: "secret-sticker-id", Description: "震惊和无语", MIMEType: "image/png",
			}},
			{Kind: historyexpr.Utterance, Text: "后一句。", VisualState: "idle"},
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
