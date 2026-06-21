package runtime

import (
	"testing"

	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestSceneGenerateRequestFromRecordRestoresMaterialContextWithoutUnsupportedSource(t *testing.T) {
	record := app.SessionRecord{
		Characters: []app.Character{{
			ID:          "atri",
			DisplayName: "亚托莉",
		}},
		Teaching: app.TeachingSnapshot{
			Topic:        "Go 调度器",
			LearningGoal: "理解 GMP 模型。",
			MaterialSource: app.MaterialSource{
				Mode: app.MaterialSourceMode("unsupported"),
			},
			MaterialContext: app.MaterialContext{
				Text:  "# GMP\nG、M、P 是调度器的核心结构。",
				Brief: "# GMP\nG、M、P 是调度器的核心结构。",
				Report: app.MaterialSourceReport{
					Mode:  app.MaterialSourceMode("unsupported"),
					Items: []app.MaterialItem{{Path: "README.md", TextBytes: 42}},
				},
			},
		},
		Generation: app.SceneGeneration{
			Request: app.SceneGenerateRequest{},
		},
	}

	req := sceneGenerateRequestFromRecord(record)
	if req.MaterialSource.Mode != "" {
		t.Fatalf("material source mode = %q, want empty for unsupported legacy source", req.MaterialSource.Mode)
	}
	if req.MaterialContext.Brief == "" {
		t.Fatal("material context brief was not restored")
	}
	if app.SceneGenerateMaterialText(req) == "" {
		t.Fatal("material text should be readable from material context")
	}
}
