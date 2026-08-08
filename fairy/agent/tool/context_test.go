package tool

import (
	"strings"
	"testing"
	"time"

	"fairy/context/character"
	"fairy/runtime/model"
)

func TestToolContextSegmentsPreservePairingAndOrder(t *testing.T) {
	created := time.UnixMilli(1000)
	segments, err := ContextSegments([]model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: "call-1", ToolName: StickerSearch, ToolArguments: `{}`},
		{Type: model.PromptItemToolResult, ToolCallID: "call-1", Content: `{"candidates":[]}`},
	}, created)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || segments[1].Dependencies[0] != segments[0].ID {
		t.Fatalf("segments = %#v", segments)
	}
	if segments[1].Recoverability != model.ContextRecoverabilityRefetchable ||
		segments[1].ExpiresAtUnixMS == nil ||
		*segments[1].ExpiresAtUnixMS != created.Add(ResultSegmentTTL).UnixMilli() {
		t.Fatalf("result segment = %#v", segments[1])
	}
	items, err := (character.ContextProjector{}).ProjectSlotsWithTail(nil, segments)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Type != model.PromptItemToolCall ||
		items[1].Type != model.PromptItemToolResult ||
		items[0].ToolCallID != items[1].ToolCallID {
		t.Fatalf("items = %#v", items)
	}
}

func TestDesktopToolContextRemainsEphemeral(t *testing.T) {
	parts := model.PromptContentParts{
		{Type: model.PromptContentText, Text: "desktop evidence"},
		{Type: model.PromptContentImage, ImageDataURL: "data:image/png;base64,SENSITIVE", ImageMIME: "image/png"},
	}
	items := []model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: "call-vision", ToolName: DesktopObserve, ToolArguments: `{}`},
		{Type: model.PromptItemToolResult, ToolCallID: "call-vision", Parts: &parts},
	}
	segments, err := ContextSegments(items, time.UnixMilli(1000))
	if err != nil {
		t.Fatal(err)
	}
	if segments[1].Recoverability != model.ContextRecoverabilityEphemeral {
		t.Fatalf("recoverability = %q", segments[1].Recoverability)
	}
	if segments[1].ContentRef != "" || !strings.Contains((*segments[1].Item.Parts)[1].ImageDataURL, "SENSITIVE") {
		t.Fatalf("desktop result segment = %#v", segments[1])
	}
}

func TestToolContextSegmentsRejectMismatchedCallIDs(t *testing.T) {
	_, err := ContextSegments([]model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: "call-1", ToolName: StickerSearch},
		{Type: model.PromptItemToolResult, ToolCallID: "call-2", Content: "{}"},
	}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "ids must match") {
		t.Fatalf("toolContextSegments() error = %v", err)
	}
}
