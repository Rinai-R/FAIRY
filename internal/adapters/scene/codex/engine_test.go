package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestBuildPromptRequiresPlayerDrivenTeachingScene(t *testing.T) {
	t.Parallel()

	req := app.SceneGenerateRequest{
		Topic:        "注意力机制",
		DocumentText: "注意力机制用于让模型关注输入中的重要信息。",
		LearningGoal: "能解释 token 之间如何互相参考。",
		Characters: []app.Character{
			{ID: "tutor", DisplayName: "讲解者", Persona: "温和清晰"},
		},
	}
	prompt := buildPrompt(req, `{"request":{"topic":"注意力机制"}}`)

	for _, want := range []string{
		"材料视觉小说编排 Agent",
		"不要替玩家发言",
		"JSON schema 中 workflow.nodes 是内部字段名",
		"场景必须围绕文档内容和学习目标",

		"speech_text 做角色化发声本地化",
		"只返回符合 schema 的 JSON 内容",
		"注意力机制",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestNormalizeResponseFillsRequiredSceneRuntimeFields(t *testing.T) {
	t.Parallel()

	req := app.SceneGenerateRequest{
		Topic:        "RAG",
		DocumentText: "RAG 通过检索外部资料增强回答。",
		LearningGoal: "理解检索增强生成的基本流程。",
		Characters: []app.Character{
			{
				ID:          "tutor",
				DisplayName: "讲解者",
				Assets: app.CharacterAssets{
					ReferenceImageURL: "/ref/tutor.png",
				},
			},
			{ID: "skeptic", DisplayName: "追问者"},
		},
	}

	out := normalizeResponse(req, app.SceneGenerateResponse{
		Scene: app.Scene{
			Variables: map[string]string{"outline": "先问玩家对 RAG 的直觉。"},
		},
		Session: app.Session{
			ParticipantIDs: []string{"player", "tutor", "skeptic"},
		},
		OpeningMessage: "复杂度判定：Trivial\n我们开始吧。",
		Prompt: app.PromptConfig{
			System:    "自定义系统提示",
			Developer: "复杂度判定：Trivial，执行路径：直接输出。\n保持角色教学。",
		},
	})

	if out.Scene.ID == "" || out.Scene.Title == "" || out.Scene.LastActiveAt.IsZero() {
		t.Fatalf("scene fields not filled: %#v", out.Scene)
	}
	if out.Scene.Variables["topic"] != "RAG" {
		t.Fatalf("topic variable = %q", out.Scene.Variables["topic"])
	}
	if out.Scene.Variables["outline"] == "" {
		t.Fatalf("outline variable missing: %#v", out.Scene.Variables)
	}
	if out.Session.ActiveCharacterID != "tutor" {
		t.Fatalf("active character = %q", out.Session.ActiveCharacterID)
	}
	if len(out.Session.ParticipantIDs) != 2 {
		t.Fatalf("participant ids = %#v", out.Session.ParticipantIDs)
	}
	if out.Session.ParticipantIDs[0] != "tutor" || out.Session.ParticipantIDs[1] != "skeptic" {
		t.Fatalf("participant ids should only keep characters: %#v", out.Session.ParticipantIDs)
	}
	if strings.Contains(out.OpeningMessage, "复杂度判定") || strings.Contains(out.Prompt.Developer, "执行路径") {
		t.Fatalf("internal meta was not stripped: %#v / %#v", out.OpeningMessage, out.Prompt.Developer)
	}
	if out.Image.ReferenceImageURL != "/ref/tutor.png" {
		t.Fatalf("reference image = %q", out.Image.ReferenceImageURL)
	}
	if !strings.Contains(out.Prompt.ResponseContract, "角色口吻") {
		t.Fatalf("response contract = %q", out.Prompt.ResponseContract)
	}
}

func TestNormalizeResponseRebuildsThinTeachingWorkflow(t *testing.T) {
	t.Parallel()

	req := app.SceneGenerateRequest{
		Topic:        "系统分析方法",
		DocumentText: "系统分析需要先确定问题边界，再分析角色、流程、约束和反馈。",
		LearningGoal: "能按材料主线解释系统分析步骤。",
		Characters: []app.Character{
			{ID: "tutor", DisplayName: "亚托莉"},
		},
	}

	out := normalizeResponse(req, app.SceneGenerateResponse{
		Workflow: app.TeachingWorkflow{
			ID:    "thin",
			Title: "太短的剧情",
			Nodes: []app.TeachingWorkflowNode{
				{ID: "opening", Kind: "opening", Title: "开场", Speaker: "亚托莉", Line: "我们开始吧。", NextNodeID: "free"},
				{ID: "free", Kind: "free_discussion", Title: "自由讨论", Speaker: "亚托莉", Line: "现在你可以提问。", FreeDiscussion: true},
			},
		},
	})

	if len(out.Workflow.Nodes) < 7 {
		t.Fatalf("workflow nodes = %d, want rebuilt long teaching script", len(out.Workflow.Nodes))
	}
	if out.Workflow.Nodes[len(out.Workflow.Nodes)-1].Kind != "free_discussion" {
		t.Fatalf("last kind = %q, want free_discussion", out.Workflow.Nodes[len(out.Workflow.Nodes)-1].Kind)
	}
	lessonCount := 0
	for _, node := range out.Workflow.Nodes {
		if node.Kind == "lesson" || node.Kind == "challenge" {
			lessonCount++
		}
	}
	if lessonCount < 3 {
		t.Fatalf("lesson count = %d, want at least 3", lessonCount)
	}
}

func TestNormalizeWorkflowPreservesStructuredDialogueLines(t *testing.T) {
	t.Parallel()

	workflow := normalizeWorkflow(app.TeachingWorkflow{
		Nodes: []app.TeachingWorkflowNode{
			{
				ID:      "opening",
				Kind:    "opening",
				Title:   "开场",
				Speaker: "亚托莉",
				Lines: []app.DialogueLine{
					{Speaker: "亚托莉", Text: "我们先看问题从哪里来。", SpeechText: "まず問題がどこから来るのか見ていきましょう。", Expression: "soft_smile"},
					{Speaker: "追问者", Text: "也就是说先别急着背定义？", SpeechText: "つまり、まだ定義を暗記しなくていいんですね。", Expression: "curious"},
				},
				NextNodeID: "lesson-1",
			},
			{
				ID:      "lesson-1",
				Kind:    "lesson",
				Title:   "第一幕",
				Speaker: "亚托莉",
				Lines: []app.DialogueLine{
					{Speaker: "亚托莉", Text: "第一点。", SpeechText: "一点目です。", Expression: "thinking"},
					{Speaker: "追问者", Text: "能不能举个例子？", SpeechText: "例を挙げてもらえますか。", Expression: "curious"},
				},
				NextNodeID: "free-1",
			},
			{
				ID:             "free-1",
				Kind:           "free_discussion",
				Title:          "自由讨论 1",
				Speaker:        "亚托莉",
				FreeDiscussion: true,
				NextNodeID:     "lesson-2",
			},
			{
				ID:      "lesson-2",
				Kind:    "lesson",
				Title:   "第二幕",
				Speaker: "亚托莉",
				Lines: []app.DialogueLine{
					{Speaker: "亚托莉", Text: "第二点。", SpeechText: "二点目です。", Expression: "thinking"},
					{Speaker: "追问者", Text: "所以它和前面是递进关系？", SpeechText: "つまり前の話から発展しているんですね。", Expression: "curious"},
				},
				NextNodeID: "summary",
			},
			{
				ID:         "summary",
				Kind:       "summary",
				Title:      "总结",
				Speaker:    "亚托莉",
				NextNodeID: "free-discussion",
			},
			{
				ID:             "free-discussion",
				Kind:           "free_discussion",
				Title:          "自由讨论",
				Speaker:        "亚托莉",
				FreeDiscussion: true,
			},
		},
	}, "lesson-x", "topic", "goal", "doc", app.Character{DisplayName: "亚托莉"}, app.SceneInteraction{})

	if len(workflow.Nodes) == 0 || len(workflow.Nodes[0].Lines) == 0 {
		t.Fatalf("structured lines lost: %#v", workflow.Nodes)
	}
	if workflow.Nodes[0].Lines[0].Text != "我们先看问题从哪里来。" {
		t.Fatalf("display text changed unexpectedly: %#v", workflow.Nodes[0].Lines[0])
	}
	if workflow.Nodes[0].Lines[0].SpeechText != "まず問題がどこから来るのか見ていきましょう。" {
		t.Fatalf("speech text changed unexpectedly: %#v", workflow.Nodes[0].Lines[0])
	}
}

func TestNormalizeResponseRejectsPrematureFreeDiscussion(t *testing.T) {
	t.Parallel()

	longLine := "这一幕先把材料里的问题慢慢展开，不急着让玩家自由提问，而是通过角色对白解释直觉、术语和具体关系，让主线能够继续往下走。"
	req := app.SceneGenerateRequest{
		Topic:        "注意力机制",
		DocumentText: "注意力机制让模型关注输入中的重要信息。",
		Characters:   []app.Character{{ID: "tutor", DisplayName: "亚托莉"}},
	}

	out := normalizeResponse(req, app.SceneGenerateResponse{
		Workflow: app.TeachingWorkflow{
			Nodes: []app.TeachingWorkflowNode{
				{ID: "opening", Kind: "opening", Title: "开场", Speaker: "亚托莉", Line: longLine, NextNodeID: "lesson-1"},
				{ID: "lesson-1", Kind: "lesson", Title: "第一幕", Speaker: "亚托莉", Line: longLine, NextNodeID: "free"},
				{ID: "free", Kind: "free_discussion", Title: "自由讨论", Speaker: "亚托莉", Line: longLine, FreeDiscussion: true, NextNodeID: "lesson-2"},
				{ID: "lesson-2", Kind: "lesson", Title: "第二幕", Speaker: "亚托莉", Line: longLine, NextNodeID: "lesson-3"},
				{ID: "lesson-3", Kind: "lesson", Title: "第三幕", Speaker: "亚托莉", Line: longLine, NextNodeID: "choice"},
				{ID: "choice", Kind: "choice", Title: "确认", Speaker: "亚托莉", Line: longLine, NextNodeID: "summary"},
				{ID: "summary", Kind: "summary", Title: "总结", Speaker: "亚托莉", Line: longLine},
			},
		},
	})

	last := out.Workflow.Nodes[len(out.Workflow.Nodes)-1]
	if last.Kind != "free_discussion" {
		t.Fatalf("last kind = %q, want rebuilt free_discussion last", last.Kind)
	}
}

func TestGenerateRejectsMissingDocumentAndCharacters(t *testing.T) {
	t.Parallel()

	engine := NewEngine(Options{CodexBin: "codex"})
	if _, err := engine.Generate(context.Background(), scene.Input{Request: app.SceneGenerateRequest{}}); err == nil {
		t.Fatal("Generate() error = nil, want missing document error")
	}
	if _, err := engine.Generate(context.Background(), scene.Input{Request: app.SceneGenerateRequest{
		DocumentText: "材料",
	}}); err == nil {
		t.Fatal("Generate() error = nil, want missing characters error")
	}
}

func TestSceneSchemaAvoidsUnsupportedLooseKeywords(t *testing.T) {
	t.Parallel()

	for _, forbidden := range []string{
		`"format"`,
		`"workflow": true`,
	} {
		if strings.Contains(sceneSchema, forbidden) {
			t.Fatalf("scene schema contains unsupported keyword %s", forbidden)
		}
	}
}
