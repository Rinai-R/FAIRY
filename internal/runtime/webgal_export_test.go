package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/runtime"
)

func TestExportWebGALCompilesChoiceScene(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{})

	resp, err := rt.ExportWebGAL(context.Background(), app.WebGALExportRequest{
		Scene: app.Scene{
			ID:       "lesson-attention",
			Title:    "文档教学：注意力机制",
			Location: "classroom",
			Variables: map[string]string{
				"topic":         "注意力机制",
				"learning_goal": "能解释注意力机制解决什么问题",
			},
			LastActiveAt: time.Now(),
		},
		Characters: []app.Character{{
			ID:          "tutor",
			DisplayName: "亚托莉",
			Assets: app.CharacterAssets{
				PortraitURL:   "/images/fairy.png",
				BackgroundURL: "/images/classroom.png",
			},
		}},
		OpeningMessage: "我们开始吧；先不要急着背公式。",
		Interaction: app.SceneInteraction{
			Mode: "choice",
			Choices: []app.SceneChoice{
				{ID: "summary", Label: "先讲直觉", Text: "请先用直觉解释。"},
				{ID: "challenge", Label: "我来反驳", Text: "我想挑战这个说法。"},
			},
		},
	})
	if err != nil {
		t.Fatalf("ExportWebGAL() error = %v", err)
	}
	if resp.EntryFile != "start.txt" {
		t.Fatalf("EntryFile = %q, want start.txt", resp.EntryFile)
	}
	for _, want := range []string{
		"changeBg:classroom.png;",
		"changeFigure:fairy.png -center;",
		"亚托莉:我们开始吧；先不要急着背公式。;",
		"choose:先讲直觉:summary|我来反驳:challenge;",
		"label:summary;",
		"jumpLabel:workflow_summary;",
	} {
		if !strings.Contains(resp.Script, want) {
			t.Fatalf("script missing %q:\n%s", want, resp.Script)
		}
	}
	if resp.Files["start.txt"] != resp.Script {
		t.Fatal("Files[start.txt] does not match Script")
	}
}

func TestExportWebGALCompilesTeachingWorkflow(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{})

	resp, err := rt.ExportWebGAL(context.Background(), app.WebGALExportRequest{
		Scene: app.Scene{
			ID:       "lesson-workflow",
			Title:    "文档教学：工作流",
			Location: "classroom",
			Variables: map[string]string{
				"topic":         "工作流",
				"learning_goal": "理解教学阶段如何推进",
			},
			LastActiveAt: time.Now(),
		},
		Characters:     []app.Character{{ID: "tutor", DisplayName: "亚托莉", Assets: app.CharacterAssets{BackgroundURL: "/images/classroom.png"}}},
		OpeningMessage: "我们把材料拆成几个可推进的节点。",
		Workflow: app.TeachingWorkflow{
			ID:            "wf-1",
			Title:         "教学工作流",
			Goal:          "理解教学阶段如何推进",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				{ID: "opening", Kind: "opening", Title: "开场", Speaker: "亚托莉", Line: "先搭好舞台。", NextNodeID: "choice"},
				{
					ID:         "choice",
					Kind:       "choice",
					Title:      "选择",
					Speaker:    "亚托莉",
					Line:       "你想怎么推进？",
					NextNodeID: "free-discussion",
					Choices: []app.SceneChoice{
						{ID: "example", Label: "举例", Text: "请举例。"},
					},
				},
				{ID: "free-discussion", Kind: "free_discussion", Title: "自由讨论", Speaker: "亚托莉", Line: "现在你可以自由提问。", FreeDiscussion: true, NextNodeID: "summary"},
				{ID: "summary", Kind: "summary", Title: "总结", Speaker: "亚托莉", Line: "我们总结一下。"},
			},
			History: []app.WorkflowHistoryItem{
				{NodeID: "opening", AudioURL: "/audio/opening.mp3", AudioFormat: "mp3", OccurredAt: time.Now()},
			},
		},
		Image: app.ImageRequest{ReferenceImageURL: "/images/classroom.png"},
	})
	if err != nil {
		t.Fatalf("ExportWebGAL() error = %v", err)
	}
	for _, want := range []string{
		"; workflow:wf-1",
		"label:opening;",
		"; audio:opening.mp3",
		"亚托莉:先搭好舞台。 -vocal=opening.mp3;",
		"jumpLabel:choice;",
		"choose:举例:free-discussion;",
		"; free-discussion:free-discussion",
		"label:summary;",
	} {
		if !strings.Contains(resp.Script, want) {
			t.Fatalf("script missing %q:\n%s", want, resp.Script)
		}
	}
}

func TestExportWebGALRejectsInvalidRequest(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{})
	_, err := rt.ExportWebGAL(context.Background(), app.WebGALExportRequest{})
	if err == nil {
		t.Fatal("ExportWebGAL() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "scene.id") {
		t.Fatalf("error = %q, want scene.id", err)
	}
}
