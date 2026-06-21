package mock

import (
	"context"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func textMaterial(text string) app.MaterialSource {
	return app.MaterialSource{Mode: app.MaterialSourceText, Text: text}
}

func TestMockAgentDiscussCanPreviewDifferentSpeechLanguage(t *testing.T) {
	t.Parallel()

	out, err := MockEngine{}.Discuss(context.Background(), agent.DiscussInput{
		Turn: app.TurnRequest{
			Character: app.Character{ID: "tutor", VoiceID: "S_test"},
			Runtime: app.RuntimeConfig{
				Language: app.LanguagePlan{
					DisplayLanguage: "zh-CN",
					SpeechLanguage:  "ja",
					Mode:            "translate_for_voice",
				},
			},
			User: app.UserInput{UserID: "default", Text: "注意力是什么？"},
		},
	})
	if err != nil {
		t.Fatalf("Discuss() error = %v", err)
	}
	if out.DisplayText == "" || out.SpeechText == "" {
		t.Fatalf("display/speech text missing: %#v", out)
	}
	if out.DisplayText == out.SpeechText {
		t.Fatalf("DisplayText and SpeechText should differ in translate_for_voice mode")
	}
	if !strings.Contains(out.SpeechText, "資料の流れ") {
		t.Fatalf("SpeechText = %q, want Japanese preview text", out.SpeechText)
	}
}

func TestMockAgentGenerateActReturnsPlayableTeachingAct(t *testing.T) {
	t.Parallel()

	out, err := MockEngine{}.GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			Topic:        "注意力机制",
			LearningGoal: "理解注意力为什么要给不同信息分配权重。",
			Characters:   []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		ActIndex: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if out.Decision != agent.ActDecisionContinue {
		t.Fatalf("Decision = %q, want continue", out.Decision)
	}
	if out.Node.ID != "opening" {
		t.Fatalf("node.ID = %q, want opening", out.Node.ID)
	}
	if len(out.Node.Lines) < 3 {
		t.Fatalf("lines = %d, want >= 3", len(out.Node.Lines))
	}
	if len(out.Node.Choices) < 1 || len(out.Node.Choices) > 3 {
		t.Fatalf("choices = %d, want 1..3", len(out.Node.Choices))
	}
	if out.Node.Summary == "" || len(out.CoveredPoints) == 0 {
		t.Fatalf("teaching structure missing: %#v", out)
	}
}

func TestMockAgentGenerateActUsesChoice(t *testing.T) {
	t.Parallel()

	out, err := MockEngine{}.GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		Choice:   app.SceneChoice{ID: "example", Label: "先看例子"},
		ActIndex: 2,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if !strings.Contains(out.Node.Summary, "先看例子") {
		t.Fatalf("node summary = %q, want choice label", out.Node.Summary)
	}
}

func TestMockAgentGenerateActIgnoresGenericPlannedTitle(t *testing.T) {
	t.Parallel()

	out, err := MockEngine{}.GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("Go 调度器的核心是 GMP 模型。G 表示 goroutine，M 表示系统线程，P 表示执行上下文。\n全局队列和本地队列共同决定 goroutine 的调度顺序。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 2,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if out.Node.Summary == "第一幕" {
		t.Fatalf("node summary leaked planned title: %#v", out.Node)
	}
	if !strings.Contains(out.Node.Summary, "调度") {
		t.Fatalf("node summary = %q, want document-derived point", out.Node.Summary)
	}
}

func TestMockAgentDiscussIsSeparateMethod(t *testing.T) {
	t.Parallel()

	out, err := MockEngine{}.Discuss(context.Background(), agent.DiscussInput{
		Turn: app.TurnRequest{
			Character: app.Character{ID: "atri", VoiceID: "S_test"},
			User:      app.UserInput{UserID: "default", Text: "这里为什么要这样做？"},
		},
	})
	if err != nil {
		t.Fatalf("Discuss() error = %v", err)
	}
	if out.DisplayText == "" || out.SpeechText == "" {
		t.Fatalf("Discuss output missing text: %#v", out)
	}
	if !strings.Contains(out.DisplayText, "这里为什么要这样做") {
		t.Fatalf("DisplayText = %q, want user question", out.DisplayText)
	}
}
