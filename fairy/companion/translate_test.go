package companion

import (
	"strings"
	"testing"

	"fairy/character"
	"fairy/model"
)

func TestBuildTranslateInputIncludesCharacterSpeechPrefix(t *testing.T) {
	style := "短句、句尾带ね。"
	items, err := BuildTranslateInput(character.Record{
		CharacterID:      "character-1",
		Revision:         3,
		Name:             "亚托莉",
		Description:      "认真听用户说话。",
		DialogueStyle:    &style,
		TextLanguage:     "zh",
		SpeakingLanguage: "ja",
	}, "嗯，我懂。先这样改。", "zh", "ja")
	if err != nil {
		t.Fatalf("BuildTranslateInput() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].Type != model.PromptItemContextData {
		t.Fatalf("prefix type = %q", items[0].Type)
	}
	for _, needle := range []string{`"contextType":"character"`, "亚托莉", "短句", `"textLanguage":"zh"`, `"speakingLanguage":"ja"`} {
		if !strings.Contains(items[0].Content, needle) {
			t.Fatalf("character prefix missing %q in %s", needle, items[0].Content)
		}
	}
	if items[1].Type != model.PromptItemUserMessage {
		t.Fatalf("task type = %q", items[1].Type)
	}
	for _, needle := range []string{"Source language: Chinese (zh)", "Target speaking language: Japanese (ja)", "嗯，我懂。先这样改。", "spoken Japanese"} {
		if !strings.Contains(items[1].Content, needle) {
			t.Fatalf("task missing %q in %s", needle, items[1].Content)
		}
	}
	if strings.Contains(items[1].Content, `"contextType":"speech_translate"`) {
		t.Fatalf("task should not use opaque speech_translate JSON blob: %s", items[1].Content)
	}
}

func TestBuildTranslateInputRejectsEmptyDisplayText(t *testing.T) {
	_, err := BuildTranslateInput(character.Record{Name: "亚托莉", Description: "认真听用户说话。"}, "  ", "zh", "ja")
	if err == nil {
		t.Fatal("BuildTranslateInput() error = nil, want empty display rejection")
	}
}

func TestSummarizeStreamEvents(t *testing.T) {
	got := summarizeStreamEvents([]model.StreamEvent{
		{Type: "usage"},
		{Type: "completed"},
		{Type: "usage"},
	})
	if got != "usage=2,completed=1" {
		t.Fatalf("summarizeStreamEvents() = %q", got)
	}
	if summarizeStreamEvents(nil) != "no events" {
		t.Fatalf("empty summary = %q", summarizeStreamEvents(nil))
	}
}
