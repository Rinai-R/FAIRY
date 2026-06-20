package runtime

import (
	"testing"

	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestSceneGenerateRequestFromRecordRestoresMaterialContext(t *testing.T) {
	record := app.SessionRecord{
		Characters: []app.Character{{
			ID:          "atri",
			DisplayName: "亚托莉",
		}},
		Teaching: app.TeachingSnapshot{
			Topic:        "Go 调度器",
			LearningGoal: "理解 GMP 模型。",
			MaterialSource: app.MaterialSource{
				Mode: app.MaterialSourceLocalDirectory,
				Path: "/Users/rinai/project/demo",
			},
			MaterialContext: app.MaterialContext{
				Text:  "# GMP\nG、M、P 是调度器的核心结构。",
				Brief: "# GMP\nG、M、P 是调度器的核心结构。",
				Report: app.MaterialSourceReport{
					Mode:  app.MaterialSourceLocalDirectory,
					Items: []app.MaterialItem{{Path: "README.md", TextBytes: 42}},
				},
			},
		},
		Generation: app.SceneGeneration{
			Request: app.SceneGenerateRequest{},
		},
	}

	req := sceneGenerateRequestFromRecord(record)
	if req.MaterialSource.Mode != app.MaterialSourceLocalDirectory {
		t.Fatalf("material source mode = %q, want local_directory", req.MaterialSource.Mode)
	}
	if req.MaterialSource.Path != "/Users/rinai/project/demo" {
		t.Fatalf("material source path = %q", req.MaterialSource.Path)
	}
	if req.MaterialContext.Brief == "" {
		t.Fatal("material context brief was not restored")
	}
	if req.DocumentText == "" {
		t.Fatal("document text should keep a compatibility copy from material context")
	}
}
