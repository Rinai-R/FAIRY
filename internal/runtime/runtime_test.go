package runtime_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	agentmock "github.com/Rinai-R/FAIRY/internal/adapters/agent/mock"
	"github.com/Rinai-R/FAIRY/internal/adapters/image"
	imagemock "github.com/Rinai-R/FAIRY/internal/adapters/image/mock"
	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	scenemock "github.com/Rinai-R/FAIRY/internal/adapters/scene/mock"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/runtime"
)

const testVoiceProvider voice.Provider = "test-voice"

func textMaterial(text string) app.MaterialSource {
	return app.MaterialSource{Mode: app.MaterialSourceText, Text: text}
}

func TestSceneGenerateRequestJSONUsesMaterialSourceOnly(t *testing.T) {
	legacyText := "旧请求文本不应出现在 JSON 合同里"
	raw, err := json.Marshal(app.SceneGenerateRequest{
		Topic:          "Go 调度器",
		MaterialSource: textMaterial("粘贴正文通过 material_source.text 进入生成链路"),
		Characters:     []app.Character{{ID: "tutor"}},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "document_text") || strings.Contains(text, legacyText) {
		t.Fatalf("SceneGenerateRequest JSON exposed document_text field: %s", text)
	}
	if !strings.Contains(text, `"material_source"`) || !strings.Contains(text, `"text"`) {
		t.Fatalf("SceneGenerateRequest JSON missing material_source text contract: %s", text)
	}
}

func TestMaterialSourceJSONHasNoURLDirectoryFields(t *testing.T) {
	materialType := reflect.TypeOf(app.MaterialSource{})
	for _, fieldName := range []string{"URL", "Path", "DisplayName", "LocalDirectory", "DocumentURL", "SourceURL"} {
		if _, ok := materialType.FieldByName(fieldName); ok {
			t.Fatalf("MaterialSource must not expose legacy field %s", fieldName)
		}
	}
	raw, err := json.Marshal(app.MaterialSource{
		Mode:      app.MaterialSourceUploadedFile,
		AssetID:   "asset-1",
		AssetName: "lesson.md",
		AssetType: "text/markdown",
		AssetPath: "materials/lesson.md",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(raw)
	for _, token := range []string{"url", "path_url", "document_url", "source_url", "local_directory", "display_name"} {
		if strings.Contains(text, token) {
			t.Fatalf("MaterialSource JSON exposed legacy token %q: %s", token, text)
		}
	}
}

func TestGenerateScenePersistsTeachingSessionAndTurns(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		Images: map[image.Provider]image.Engine{
			image.ProviderMock: imagemock.Engine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		DefaultScene: scene.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	characters := []app.Character{
		{
			ID:          "tutor",
			DisplayName: "Tutor",
			VoiceID:     "mock",
			Persona:     "负责讲解文档概念",
			Assets: app.CharacterAssets{
				PortraitURL:       "/assets/tutor/default.png",
				ReferenceImageURL: "/assets/tutor/reference.png",
				StylePrompt:       "soft visual novel tutor",
				Moods: map[string]app.CharacterMood{
					"welcoming": {PortraitURL: "/assets/tutor/welcoming.png", CGPrompt: "welcoming smile"},
				},
			},
		},
	}
	sceneResp, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:          "注意力机制",
		MaterialSource: textMaterial("# 注意力机制\n注意力机制用于让模型关注输入中的重要信息。"),
		LearningGoal:   "能够解释注意力机制解决什么问题。",
		Characters:     characters,
		Runtime: app.RuntimeConfig{
			SceneProvider: string(scene.ProviderMock),
		},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}

	record, err := rt.Session(sceneResp.Session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if record.Scene.ID != sceneResp.Scene.ID {
		t.Fatalf("persisted scene ID = %q, want %q", record.Scene.ID, sceneResp.Scene.ID)
	}
	if len(record.Characters) != len(characters) {
		t.Fatalf("persisted characters = %d, want %d", len(record.Characters), len(characters))
	}
	if record.Characters[0].Assets.ReferenceImageURL != "/assets/tutor/reference.png" {
		t.Fatalf("persisted character reference = %q", record.Characters[0].Assets.ReferenceImageURL)
	}
	if record.Workflow.ID == "" {
		t.Fatal("persisted workflow ID is empty")
	}
	if len(record.Workflow.Nodes) < 1 {
		t.Fatalf("persisted workflow nodes = %d, want at least 1", len(record.Workflow.Nodes))
	}
	if record.Workflow.CurrentNodeID == "" {
		t.Fatal("persisted workflow current node is empty")
	}
	currentNode := workflowNodeByID(record.Workflow.Nodes, record.Workflow.CurrentNodeID)
	if currentNode.Status != app.WorkflowNodeStatusReady {
		t.Fatalf("current node status = %q, want ready", currentNode.Status)
	}
	if len(currentNode.Lines) == 0 || currentNode.Lines[0].Audio.Format == "" {
		t.Fatalf("current node missing prepared audio: %+v", currentNode)
	}
	nextNodeID := waitForWorkflowNextNodeID(t, rt, sceneResp.Session.ID, currentNode.ID)
	if nextNodeID != "" {
		deadline := time.Now().Add(2 * time.Second)
		for {
			latest, err := rt.Session(sceneResp.Session.ID)
			if err != nil {
				t.Fatalf("Session() while waiting preload error = %v", err)
			}
			nextNode := workflowNodeByID(latest.Workflow.Nodes, nextNodeID)
			if nextNode.Status == app.WorkflowNodeStatusReady {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("next node status = %q, want background ready", nextNode.Status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if len(record.Messages) != 1 {
		t.Fatalf("opening messages = %d, want 1", len(record.Messages))
	}
	if record.Messages[0].Text != sceneResp.OpeningMessage {
		t.Fatalf("opening message = %q, want %q", record.Messages[0].Text, sceneResp.OpeningMessage)
	}
	if record.Messages[0].DisplayText != sceneResp.OpeningMessage {
		t.Fatalf("opening display_text = %q, want %q", record.Messages[0].DisplayText, sceneResp.OpeningMessage)
	}
	if record.Interaction.Mode != "dialogue" {
		t.Fatalf("persisted interaction mode = %q, want dialogue", record.Interaction.Mode)
	}

	_, err = rt.Turn(context.Background(), app.TurnRequest{
		Session:    sceneResp.Session,
		Characters: characters,
		Scene:      sceneResp.Scene,
		Relation:   sceneResp.Relation,
		Prompt:     sceneResp.Prompt,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
			ImageProvider: string(image.ProviderMock),
			Image: app.ImageRequest{
				Enabled: true,
				Prompt:  "lesson cg",
			},
		},
		User: app.UserInput{
			UserID: sceneResp.Session.UserID,
			Text:   "我能不能先用自己的话复述一下？",
			Mode:   "text",
		},
	})
	if err != nil {
		t.Fatalf("Turn() error = %v", err)
	}

	record, err = rt.Session(sceneResp.Session.ID)
	if err != nil {
		t.Fatalf("Session() after turn error = %v", err)
	}
	if len(record.Messages) != 3 {
		t.Fatalf("messages after turn = %d, want 3", len(record.Messages))
	}
	if record.Messages[1].Role != "user" {
		t.Fatalf("second message role = %q, want user", record.Messages[1].Role)
	}
	if record.Messages[2].Role != "assistant" || record.Messages[2].CharacterID != "tutor" {
		t.Fatalf("assistant reply role/character = %q/%q, want assistant/tutor", record.Messages[2].Role, record.Messages[2].CharacterID)
	}
	if record.Messages[2].SceneImagePrompt != "lesson cg" {
		t.Fatalf("assistant scene image prompt = %q, want lesson cg", record.Messages[2].SceneImagePrompt)
	}
	if record.Messages[2].DisplayText == "" || record.Messages[2].SpeechText == "" {
		t.Fatalf("assistant display/speech text missing: %#v", record.Messages[2])
	}
	imageEvent := runtimeEventByType(record.Events, app.RuntimeEventTypeImageGenerateDone)
	if imageEvent.ID == "" || imageEvent.Stage != app.RuntimeEventStageImage || imageEvent.Provider != string(image.ProviderMock) || imageEvent.DurationMS <= 0 {
		t.Fatalf("image.generate.completed event = %+v", imageEvent)
	}
	persistEvent := runtimeEventByType(record.Events, app.RuntimeEventTypePersistTurnSaved)
	if persistEvent.ID == "" || persistEvent.Stage != app.RuntimeEventStagePersist || persistEvent.DurationMS <= 0 {
		t.Fatalf("persist.turn.saved event = %+v", persistEvent)
	}
}

func TestGenerateSceneUsesAgentWithoutSceneProvider(t *testing.T) {
	agentEngine := &recordingAgentEngine{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentEngine,
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Logger:       slog.Default(),
	})

	resp, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:          "Go 调度器",
		MaterialSource: textMaterial("G、M、P 是 Go runtime 调度器的三个核心角色。"),
		LearningGoal:   "理解 GMP 三个角色的分工。",
		Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "mock"}},
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
		},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}
	if !agentEngine.called {
		t.Fatal("agent GenerateAct was not called")
	}
	if resp.Scene.ID == "" || resp.Session.ID == "" {
		t.Fatalf("scene/session id missing: scene=%q session=%q", resp.Scene.ID, resp.Session.ID)
	}
	if len(resp.Workflow.Nodes) != 1 {
		t.Fatalf("initial workflow nodes = %d, want only opening", len(resp.Workflow.Nodes))
	}
	opening := resp.Workflow.Nodes[0]
	if opening.ID != "opening" || opening.Kind != "opening" {
		t.Fatalf("opening node = %s/%s, want opening/opening", opening.ID, opening.Kind)
	}
	if opening.NextNodeID != "lesson-1" {
		t.Fatalf("opening next node = %q, want lesson-1", opening.NextNodeID)
	}
	if len(opening.Lines) == 0 || opening.Lines[0].Audio.URL == "" {
		t.Fatalf("opening missing prepared audio: %+v", opening)
	}
	if resp.OpeningMessage == "" {
		t.Fatal("opening message is empty")
	}
}

func TestCapabilitiesExposeOnlyVolcengineVoiceProvider(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{
		Voices: map[voice.Provider]voice.Engine{
			voice.ProviderVolcengine: &recordingVoiceEngine{},
		},
		DefaultVoice: voice.ProviderVolcengine,
		Logger:       slog.Default(),
	})

	capabilities := rt.Capabilities()
	voiceIDs := map[string]bool{}
	for _, item := range capabilities.Providers.Voices {
		voiceIDs[item.ID] = true
	}
	if !voiceIDs["volcengine"] {
		t.Fatalf("capabilities voices missing volcengine provider: %#v", capabilities.Providers.Voices)
	}
	if len(voiceIDs) != 1 {
		t.Fatalf("capabilities voices exposed unexpected providers: %#v", capabilities.Providers.Voices)
	}

	healthIDs := map[string]bool{}
	for _, item := range rt.ProviderHealth(context.Background()) {
		if item.Domain == "voice" {
			healthIDs[item.Provider] = true
		}
	}
	if !healthIDs["volcengine"] {
		t.Fatalf("provider health missing volcengine provider")
	}
	if len(healthIDs) != 1 {
		t.Fatalf("provider health exposed unexpected voice providers: %#v", healthIDs)
	}
}

func TestGenerateSceneRejectsInvalidTeachingBoundary(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		DefaultScene: scene.ProviderMock,
		Logger:       slog.Default(),
	})

	for _, tt := range []struct {
		name    string
		request app.SceneGenerateRequest
		want    string
	}{
		{
			name: "missing document",
			request: app.SceneGenerateRequest{
				Characters: []app.Character{{ID: "tutor"}},
			},
			want: "material_source 不能为空",
		},
		{
			name: "missing character",
			request: app.SceneGenerateRequest{
				MaterialSource: textMaterial("课程材料"),
			},
			want: "只支持 1 个角色",
		},
		{
			name: "multiple characters",
			request: app.SceneGenerateRequest{
				MaterialSource: textMaterial("课程材料"),
				Characters:     []app.Character{{ID: "tutor"}, {ID: "skeptic"}},
			},
			want: "只支持 1 个角色",
		},
		{
			name: "empty character id",
			request: app.SceneGenerateRequest{
				MaterialSource: textMaterial("课程材料"),
				Characters:     []app.Character{{DisplayName: "Tutor"}},
			},
			want: "characters[0].id 不能为空",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rt.GenerateScene(context.Background(), tt.request)
			if err == nil {
				t.Fatal("GenerateScene() error = nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want contains %q", err, tt.want)
			}
		})
	}
}

func TestGenerateSceneMergesPlannedWorkflowNodeScaffold(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderFairy: scaffoldlessAgent{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		DefaultAgent: agent.ProviderFairy,
		DefaultVoice: testVoiceProvider,
		DefaultScene: scene.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	resp, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:          "Go 调度器",
		MaterialSource: textMaterial("Go 调度器会管理 goroutine、M 和 P。"),
		LearningGoal:   "理解 GMP",
		Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "mock"}},
		Runtime: app.RuntimeConfig{
			SceneProvider: string(scene.ProviderMock),
			AgentProvider: string(agent.ProviderFairy),
			VoiceProvider: string(testVoiceProvider),
		},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}

	node := workflowNodeByID(resp.Workflow.Nodes, resp.Workflow.CurrentNodeID)
	if node.ID == "" || node.Kind == "" || node.Title == "" {
		t.Fatalf("merged node scaffold missing: %+v", node)
	}
	if len(node.Lines) != 4 {
		t.Fatalf("lines = %d, want 4", len(node.Lines))
	}
	waitForWorkflowSettled(t, rt, resp.Session.ID)
}

func TestAdvanceWorkflowPersistsCurrentNode(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "剧情推进",
		Characters: []app.Character{{ID: "tutor", DisplayName: "Tutor"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:default", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				readyWorkflowNode("opening", "opening", "lesson-1"),
				readyWorkflowNode("lesson-1", "lesson", ""),
			},
		},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	advanced, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:default",
		CurrentNodeID: "opening",
		NextNodeID:    "lesson-1",
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow() error = %v", err)
	}
	if !advanced.Ready || advanced.Waiting {
		t.Fatalf("advance ready flags = ready:%v waiting:%v", advanced.Ready, advanced.Waiting)
	}
	if advanced.Workflow.CurrentNodeID != "lesson-1" {
		t.Fatalf("current node = %q, want lesson-1", advanced.Workflow.CurrentNodeID)
	}
	if advanced.Node.ID != "lesson-1" {
		t.Fatalf("response node = %q, want lesson-1", advanced.Node.ID)
	}

	record, err := rt.Session("lesson:default")
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if record.Workflow.CurrentNodeID != "lesson-1" {
		t.Fatalf("persisted current node = %q, want lesson-1", record.Workflow.CurrentNodeID)
	}
	if record.Scene.Phase != "lesson" {
		t.Fatalf("scene phase = %q, want lesson", record.Scene.Phase)
	}
}

func TestAdvanceWorkflowReplayAppendsHistory(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})
	readyNode := func(id string, next string) app.TeachingWorkflowNode {
		return app.TeachingWorkflowNode{
			ID:          id,
			Kind:        "lesson",
			Title:       id,
			NextNodeID:  next,
			Status:      app.WorkflowNodeStatusReady,
			VoiceStatus: app.WorkflowNodeStatusReady,
			Lines: []app.DialogueLine{{
				Text:        id,
				SpeechText:  id,
				AudioStatus: app.DialogueAudioStatusReady,
				Audio:       app.AudioResult{URL: "/audio/" + id + ".mp3", Format: "mp3"},
			}},
		}
	}
	_, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "回看跳转",
		Characters: []app.Character{{ID: "tutor", DisplayName: "Tutor"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "replay", Title: "回看跳转"},
		Session: app.Session{ID: "replay:default", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				readyNode("opening", "lesson-1"),
				readyNode("lesson-1", "lesson-2"),
				readyNode("lesson-2", ""),
			},
		},
	})
	if err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	if _, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "replay:default",
		CurrentNodeID: "opening",
		NextNodeID:    "lesson-1",
	}); err != nil {
		t.Fatalf("AdvanceWorkflow(lesson-1) error = %v", err)
	}
	if _, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "replay:default",
		CurrentNodeID: "lesson-1",
		NextNodeID:    "lesson-2",
	}); err != nil {
		t.Fatalf("AdvanceWorkflow(lesson-2) error = %v", err)
	}

	replayed, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "replay:default",
		CurrentNodeID: "lesson-2",
		NextNodeID:    "lesson-1",
		Replay:        true,
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow(replay) error = %v", err)
	}
	if replayed.Workflow.CurrentNodeID != "lesson-1" {
		t.Fatalf("current_node_id = %q, want lesson-1", replayed.Workflow.CurrentNodeID)
	}
	got := make([]string, 0, len(replayed.Workflow.History))
	for _, item := range replayed.Workflow.History {
		got = append(got, item.NodeID+":"+item.Action)
	}
	want := []string{"opening:enter", "lesson-1:advance", "lesson-2:advance", "lesson-1:replay"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("history = %#v, want %#v", got, want)
	}

	replayed, err = rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "replay:default",
		CurrentNodeID: "lesson-1",
		NextNodeID:    "lesson-1",
		Replay:        true,
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow(replay same node) error = %v", err)
	}
	got = got[:0]
	for _, item := range replayed.Workflow.History {
		got = append(got, item.NodeID+":"+item.Action)
	}
	want = []string{"opening:enter", "lesson-1:advance", "lesson-2:advance", "lesson-1:replay", "lesson-1:replay"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("history after repeated replay = %#v, want %#v", got, want)
	}
}

func TestAdvanceWorkflowRejectsChoiceNodeWithoutChoiceID(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})
	opening := readyWorkflowNode("opening", "opening", "lesson-1")
	opening.Choices = []app.SceneChoice{{
		ID:           "example",
		Label:        "先看例子",
		Text:         "分支正文不应该绕过选项直接出现。",
		TargetNodeID: "opening-choice-example",
	}}
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "选择边界",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:choice-boundary", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				opening,
				readyWorkflowNode("lesson-1", "lesson", ""),
				readyWorkflowNode("opening-choice-example", "choice", "lesson-1"),
			},
		},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	_, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:choice-boundary",
		CurrentNodeID: "opening",
		NextNodeID:    "lesson-1",
	})
	if err == nil {
		t.Fatal("AdvanceWorkflow() error = nil, want choice boundary error")
	}
	if !strings.Contains(err.Error(), "必须选择后才能推进") {
		t.Fatalf("AdvanceWorkflow() error = %v, want choice boundary message", err)
	}
	record, err := rt.Session("lesson:choice-boundary")
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if record.Workflow.CurrentNodeID != "opening" {
		t.Fatalf("current node = %q, want opening", record.Workflow.CurrentNodeID)
	}
	got := make([]string, 0, len(record.Workflow.History))
	for _, item := range record.Workflow.History {
		got = append(got, item.NodeID+":"+item.Action)
	}
	want := []string{"opening:enter"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("history = %#v, want %#v", got, want)
	}
}

func TestFileSessionStoreRejectsChoiceNodeWithoutChoiceID(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	opening := readyWorkflowNode("opening", "opening", "lesson-1")
	opening.Choices = []app.SceneChoice{{ID: "example", Label: "先看例子", TargetNodeID: "opening-choice-example"}}
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "选择边界",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:store-choice-boundary", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				opening,
				readyWorkflowNode("lesson-1", "lesson", ""),
				readyWorkflowNode("opening-choice-example", "choice", "lesson-1"),
			},
		},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	_, err := store.AdvanceWorkflow(app.WorkflowAdvanceRequest{
		SessionID:     "lesson:store-choice-boundary",
		CurrentNodeID: "opening",
		NextNodeID:    "lesson-1",
	})
	if err == nil {
		t.Fatal("AdvanceWorkflow() error = nil, want choice boundary error")
	}
	if !strings.Contains(err.Error(), "必须选择后才能推进") {
		t.Fatalf("AdvanceWorkflow() error = %v, want choice boundary message", err)
	}
}

func TestAdvanceWorkflowChoiceWaitsForMissingBranch(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentmock.MockEngine{}},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	opening := readyWorkflowNode("opening", "opening", "lesson-1")
	opening.Choices = []app.SceneChoice{{
		ID:           "example",
		Label:        "先看例子",
		Text:         "先用例子继续。",
		TargetNodeID: "opening-choice-example",
	}}
	resp := app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:default", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				opening,
				readyWorkflowNode("lesson-1", "lesson", ""),
			},
		},
	}
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, resp); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	advanced, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:default",
		CurrentNodeID: "opening",
		NextNodeID:    "opening-choice-example",
		ChoiceID:      "example",
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow(choice) error = %v", err)
	}
	if !advanced.Waiting || advanced.Ready {
		t.Fatalf("advance flags = ready:%v waiting:%v, want false/true", advanced.Ready, advanced.Waiting)
	}
	if advanced.Node.ID != "opening" || advanced.Workflow.CurrentNodeID != "opening" {
		t.Fatalf("advanced current = node:%q workflow:%q, want opening", advanced.Node.ID, advanced.Workflow.CurrentNodeID)
	}
	waitForWorkflowSettled(t, rt, "lesson:default")
}

func TestAdvanceWorkflowChoiceUsesReadyBranch(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})
	opening := readyWorkflowNode("opening", "opening", "lesson-1")
	opening.Choices = []app.SceneChoice{{
		ID:           "example",
		Label:        "先看例子",
		Text:         "先用例子继续。",
		TargetNodeID: "opening-choice-example",
	}}
	branch := readyWorkflowNode("opening-choice-example", "choice", "lesson-1")
	branch.Title = "先看例子"
	branch.Summary = "先用例子继续。"
	resp := app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:default", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				opening,
				readyWorkflowNode("lesson-1", "lesson", ""),
				branch,
			},
		},
	}
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, resp); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	advanced, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:default",
		CurrentNodeID: "opening",
		NextNodeID:    "opening-choice-example",
		ChoiceID:      "example",
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow(choice) error = %v", err)
	}
	if !advanced.Ready || advanced.Waiting || advanced.Node.ID != "opening-choice-example" {
		t.Fatalf("advanced node = %q ready=%v waiting=%v, want ready branch", advanced.Node.ID, advanced.Ready, advanced.Waiting)
	}
	if advanced.Workflow.CurrentNodeID != "opening-choice-example" {
		t.Fatalf("current node = %q, want branch", advanced.Workflow.CurrentNodeID)
	}
}

func TestAdvanceWorkflowChoiceBranchContinuesToNextNode(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})
	opening := readyWorkflowNode("opening", "opening", "lesson-1")
	opening.Choices = []app.SceneChoice{{
		ID:           "example",
		Label:        "先看例子",
		Text:         "先用例子继续。",
		TargetNodeID: "opening-choice-example",
	}}
	branch := readyWorkflowNode("opening-choice-example", "choice", "lesson-1")
	branch.Title = "先看例子"
	resp := app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:branch-next", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				opening,
				branch,
				readyWorkflowNode("lesson-1", "lesson", ""),
			},
		},
	}
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, resp); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	if _, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:branch-next",
		CurrentNodeID: "opening",
		NextNodeID:    "opening-choice-example",
		ChoiceID:      "example",
	}); err != nil {
		t.Fatalf("AdvanceWorkflow(choice) error = %v", err)
	}
	advanced, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:branch-next",
		CurrentNodeID: "opening-choice-example",
		NextNodeID:    "lesson-1",
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow(branch next) error = %v", err)
	}
	if advanced.Workflow.CurrentNodeID != "lesson-1" {
		t.Fatalf("current node = %q, want lesson-1", advanced.Workflow.CurrentNodeID)
	}
}

func TestAdvanceWorkflowCanEnterFreeDiscussionNode(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})
	free := readyWorkflowNode("free-discussion", "free_discussion", "")
	free.FreeDiscussion = true
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "自由讨论",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:free-discussion", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "summary",
			Nodes: []app.TeachingWorkflowNode{
				readyWorkflowNode("summary", "summary", "free-discussion"),
				free,
			},
		},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	advanced, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:free-discussion",
		CurrentNodeID: "summary",
		NextNodeID:    "free-discussion",
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow(free discussion) error = %v", err)
	}
	if advanced.Node.ID != "free-discussion" || !advanced.Node.FreeDiscussion || advanced.Node.Kind != "free_discussion" {
		t.Fatalf("advanced node = %+v, want free discussion", advanced.Node)
	}
}

func TestAdvanceWorkflowWaitsForPendingNode(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})
	resp := app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:default", UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				{
					ID:          "opening",
					Kind:        "opening",
					Title:       "开场",
					NextNodeID:  "lesson-1",
					Status:      app.WorkflowNodeStatusReady,
					VoiceStatus: app.WorkflowNodeStatusReady,
					Lines: []app.DialogueLine{{
						Text:        "开场",
						SpeechText:  "開場です。",
						AudioStatus: app.DialogueAudioStatusReady,
						Audio:       app.AudioResult{URL: "/audio/opening.mp3", Format: "mp3"},
					}},
				},
				{
					ID:          "lesson-1",
					Kind:        "lesson",
					Title:       "第一幕",
					Line:        "还没准备好",
					Status:      app.WorkflowNodeStatusPending,
					VoiceStatus: app.WorkflowNodeStatusPending,
				},
			},
		},
	}
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, resp); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	advanced, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:default",
		CurrentNodeID: "opening",
		NextNodeID:    "lesson-1",
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow() error = %v", err)
	}
	if !advanced.Waiting || advanced.Ready {
		t.Fatalf("advance flags = ready:%v waiting:%v", advanced.Ready, advanced.Waiting)
	}
	if advanced.Workflow.CurrentNodeID != "opening" {
		t.Fatalf("current node = %q, want opening", advanced.Workflow.CurrentNodeID)
	}
	if advanced.Node.ID != "opening" {
		t.Fatalf("response node = %q, want current opening", advanced.Node.ID)
	}
	record, err := rt.Session("lesson:default")
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if record.Workflow.CurrentNodeID != "opening" {
		t.Fatalf("persisted current node = %q, want opening", record.Workflow.CurrentNodeID)
	}
	waitForWorkflowSettled(t, rt, "lesson:default")
}

func TestGenerateSceneRejectsUnsupportedMaterialMode(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultScene: scene.ProviderMock,
		Logger:       slog.Default(),
	})

	for _, mode := range []app.MaterialSourceMode{
		app.MaterialSourceMode("url"),
		app.MaterialSourceMode("local_directory"),
		app.MaterialSourceMode("unsupported"),
	} {
		t.Run(string(mode), func(t *testing.T) {
			_, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
				Topic:      "旧材料模式",
				Characters: []app.Character{{ID: "tutor"}},
				MaterialSource: app.MaterialSource{
					Mode: mode,
				},
			})
			if err == nil {
				t.Fatal("GenerateScene() error = nil, want unsupported material mode error")
			}
			want := fmt.Sprintf(`material_source.mode 不支持: "%s"`, mode)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("GenerateScene() error = %v, want %s", err, want)
			}
		})
	}
}

func TestGenerateSceneInjectsUploadedTextAsset(t *testing.T) {
	materialDir := t.TempDir()
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultScene: scene.ProviderMock,
		MaterialDir:  materialDir,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	asset, err := rt.StoreDocumentAssetBytes(context.Background(), "lesson.md", "text/markdown", []byte("# 梯度下降\n梯度下降通过沿负梯度方向更新参数来降低损失。"))
	if err != nil {
		t.Fatalf("StoreDocumentAssetBytes() error = %v", err)
	}

	resp, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:        "梯度下降",
		LearningGoal: "理解梯度下降的更新方向。",
		Characters:   []app.Character{{ID: "tutor", DisplayName: "Tutor"}},
		MaterialSource: app.MaterialSource{
			Mode:      app.MaterialSourceUploadedFile,
			AssetName: asset.Filename,
			AssetType: asset.ContentType,
			AssetPath: asset.Path,
		},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}
	record, err := rt.Session(resp.Session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if !strings.Contains(record.Teaching.MaterialContext.Text, "沿负梯度方向更新参数") {
		t.Fatalf("teaching material context text was not injected: %q", record.Teaching.MaterialContext.Text)
	}
	if record.Teaching.MaterialSource.Mode != app.MaterialSourceUploadedFile {
		t.Fatalf("material source mode = %q, want uploaded_file", record.Teaching.MaterialSource.Mode)
	}
	if record.Teaching.MaterialContext.Report.Mode != app.MaterialSourceUploadedFile {
		t.Fatalf("material report mode = %q, want uploaded_file", record.Teaching.MaterialContext.Report.Mode)
	}
	if len(record.Teaching.MaterialContext.Report.Items) == 0 {
		t.Fatal("material report items empty")
	}
	waitForWorkflowSettled(t, rt, resp.Session.ID)
}

func TestGenerateSceneRequiresStructuredMaterialSource(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultScene: scene.ProviderMock,
		Logger:       slog.Default(),
	})

	_, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:      "旧变量路径",
		Characters: []app.Character{{ID: "tutor", DisplayName: "Tutor"}},
		Variables:  map[string]string{"imported_material": "materials/lesson.md"},
	})
	if err == nil {
		t.Fatal("GenerateScene() error = nil, want explicit material_source error")
	}
	if !strings.Contains(err.Error(), "material_source 不能为空") {
		t.Fatalf("GenerateScene() error = %v, want explicit material source message", err)
	}
}

func TestGenerateSceneExtractsUploadedPDFTextLayer(t *testing.T) {
	materialDir := t.TempDir()
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultScene: scene.ProviderMock,
		MaterialDir:  materialDir,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	asset, err := rt.StoreDocumentAssetBytes(context.Background(), "lesson.pdf", "application/pdf", simplePDFWithText("Hello GMP Scheduler"))
	if err != nil {
		t.Fatalf("StoreDocumentAssetBytes() error = %v", err)
	}

	resp, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:        "GMP",
		LearningGoal: "理解调度器。",
		Characters:   []app.Character{{ID: "tutor", DisplayName: "Tutor"}},
		MaterialSource: app.MaterialSource{
			Mode:      app.MaterialSourceUploadedFile,
			AssetName: asset.Filename,
			AssetType: asset.ContentType,
			AssetPath: asset.Path,
		},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}
	record, err := rt.Session(resp.Session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if !strings.Contains(record.Teaching.MaterialContext.Brief, "Hello GMP Scheduler") {
		t.Fatalf("material brief = %q, want PDF text", record.Teaching.MaterialContext.Brief)
	}
	waitForWorkflowSettled(t, rt, resp.Session.ID)
}

func TestGenerateSceneExtractsUploadedDOCX(t *testing.T) {
	materialDir := t.TempDir()
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultScene: scene.ProviderMock,
		MaterialDir:  materialDir,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	asset, err := rt.StoreDocumentAssetBytes(context.Background(), "lesson.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", simpleDOCXWithParagraphs("第一段：协程负责表达并发。", "第二段：通道负责同步。"))
	if err != nil {
		t.Fatalf("StoreDocumentAssetBytes() error = %v", err)
	}

	resp, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:        "Go 并发",
		LearningGoal: "理解协程和通道。",
		Characters:   []app.Character{{ID: "tutor", DisplayName: "Tutor"}},
		MaterialSource: app.MaterialSource{
			Mode:      app.MaterialSourceUploadedFile,
			AssetName: asset.Filename,
			AssetType: asset.ContentType,
			AssetPath: asset.Path,
		},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}
	record, err := rt.Session(resp.Session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if !strings.Contains(record.Teaching.MaterialContext.Brief, "通道负责同步") {
		t.Fatalf("material brief = %q, want docx content", record.Teaching.MaterialContext.Brief)
	}
	waitForWorkflowSettled(t, rt, resp.Session.ID)
}

func TestGenerateSceneRejectsUnsupportedUploadedFileType(t *testing.T) {
	materialDir := t.TempDir()
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultScene: scene.ProviderMock,
		MaterialDir:  materialDir,
		Logger:       slog.Default(),
	})
	asset, err := rt.StoreDocumentAssetBytes(context.Background(), "archive.bin", "application/octet-stream", []byte{0x00, 0x01, 0x02})
	if err != nil {
		t.Fatalf("StoreDocumentAssetBytes() error = %v", err)
	}

	_, err = rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:      "非法材料",
		Characters: []app.Character{{ID: "tutor", DisplayName: "Tutor"}},
		MaterialSource: app.MaterialSource{
			Mode:      app.MaterialSourceUploadedFile,
			AssetName: asset.Filename,
			AssetType: asset.ContentType,
			AssetPath: asset.Path,
		},
	})
	if err == nil {
		t.Fatal("GenerateScene() error = nil, want unsupported file type error")
	}
	if !strings.Contains(err.Error(), "文件类型不在材料白名单") {
		t.Fatalf("GenerateScene() error = %v, want whitelist error", err)
	}
}

func simpleDOCXWithParagraphs(paragraphs ...string) []byte {
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	file, err := writer.Create("word/document.xml")
	if err != nil {
		panic(err)
	}
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, paragraph := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t>`)
		body.WriteString(escapeXMLText(paragraph))
		body.WriteString(`</w:t></w:r></w:p>`)
	}
	body.WriteString(`</w:body></w:document>`)
	if _, err := file.Write([]byte(body.String())); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return out.Bytes()
}

func escapeXMLText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}

func simplePDFWithText(text string) []byte {
	text = strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(text)
	var out bytes.Buffer
	write := func(value string) {
		out.WriteString(value)
	}
	offsets := make([]int, 6)
	write("%PDF-1.4\n")
	writeObject := func(index int, body string) {
		offsets[index] = out.Len()
		write(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", index, body))
	}
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	writeObject(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	stream := "BT /F1 24 Tf 72 720 Td (" + text + ") Tj ET"
	writeObject(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	startXref := out.Len()
	write("xref\n0 6\n0000000000 65535 f \n")
	for index := 1; index <= 5; index++ {
		write(fmt.Sprintf("%010d 00000 n \n", offsets[index]))
	}
	write(fmt.Sprintf("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", startXref))
	return out.Bytes()
}

func TestTurnPersistsDisplayAndSpeechTextSeparately(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	voiceEngine := &recordingVoiceEngine{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: bilingualAgent{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: voiceEngine,
		},
		Images: map[image.Provider]image.Engine{
			image.ProviderMock: imagemock.Engine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		DefaultScene: scene.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	resp, err := rt.Turn(context.Background(), app.TurnRequest{
		Session:   app.Session{ID: "lesson:bilingual", UserID: "default", ActiveCharacterID: "tutor"},
		Character: app.Character{ID: "tutor", DisplayName: "Tutor", VoiceID: "S_test"},
		Scene:     app.Scene{ID: "lesson", Title: "双语测试"},
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
			ImageProvider: string(image.ProviderMock),
		},
		User: app.UserInput{UserID: "default", Text: "解释一下注意力。"},
	})
	if err != nil {
		t.Fatalf("Turn() error = %v", err)
	}
	if resp.DisplayText != "注意力会让模型关注更重要的信息。" {
		t.Fatalf("DisplayText = %q", resp.DisplayText)
	}
	if resp.SpeechText != "注意は大切な情報に目を向ける仕組みです。" {
		t.Fatalf("SpeechText = %q", resp.SpeechText)
	}
	if voiceEngine.lastText != resp.SpeechText {
		t.Fatalf("voice text = %q, want speech text %q", voiceEngine.lastText, resp.SpeechText)
	}
	record, err := rt.Session("lesson:bilingual")
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if record.Messages[1].DisplayText != resp.DisplayText {
		t.Fatalf("persisted display_text = %q", record.Messages[1].DisplayText)
	}
	if record.Messages[1].SpeechText != resp.SpeechText {
		t.Fatalf("persisted speech_text = %q", record.Messages[1].SpeechText)
	}
}

func TestTurnRejectsInvalidSingleCharacterBoundary(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Logger:       slog.Default(),
	})

	for _, tt := range []struct {
		name string
		req  app.TurnRequest
		want string
	}{
		{
			name: "multiple characters",
			req: app.TurnRequest{
				Characters: []app.Character{{ID: "tutor"}, {ID: "skeptic"}},
				User:       app.UserInput{UserID: "default", Text: "继续"},
			},
			want: "只支持 1 个角色",
		},
		{
			name: "mismatched active character",
			req: app.TurnRequest{
				Session:    app.Session{ActiveCharacterID: "skeptic"},
				Characters: []app.Character{{ID: "tutor"}},
				User:       app.UserInput{UserID: "default", Text: "继续"},
			},
			want: "active_character_id 与 character.id 不一致",
		},
		{
			name: "mismatched explicit character",
			req: app.TurnRequest{
				Characters: []app.Character{{ID: "tutor"}},
				Character:  app.Character{ID: "skeptic"},
				User:       app.UserInput{UserID: "default", Text: "继续"},
			},
			want: "character.id 与 characters[0].id 不一致",
		},
		{
			name: "mismatched participant",
			req: app.TurnRequest{
				Session:    app.Session{ParticipantIDs: []string{"skeptic"}},
				Characters: []app.Character{{ID: "tutor"}},
				User:       app.UserInput{UserID: "default", Text: "继续"},
			},
			want: "participant_ids[0] 与 character.id 不一致",
		},
		{
			name: "blank user text",
			req: app.TurnRequest{
				Character: app.Character{ID: "tutor"},
				User:      app.UserInput{UserID: "default", Text: "   "},
			},
			want: "user.text 不能为空",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rt.Turn(context.Background(), tt.req)
			if err == nil {
				t.Fatal("Turn() error = nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want contains %q", err, tt.want)
			}
		})
	}
}

func TestCloneVoiceDefaultsToVolcengineProvider(t *testing.T) {
	trainer := &cloneVoiceEngine{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Voices: map[voice.Provider]voice.Engine{
			voice.ProviderVolcengine: trainer,
		},
		DefaultVoice: testVoiceProvider,
		Logger:       slog.Default(),
	})

	result, err := rt.CloneVoice(context.Background(), app.VoiceCloneRequest{
		AppID:       "app",
		AccessToken: "token",
		ResourceID:  "seed-icl-2.0",
		SpeakerID:   "S_test",
		Language:    "ja",
	})
	if err != nil {
		t.Fatalf("CloneVoice() error = %v", err)
	}
	if result.SpeakerID != "S_test" {
		t.Fatalf("SpeakerID = %q", result.SpeakerID)
	}
	if trainer.provider != string(voice.ProviderVolcengine) {
		t.Fatalf("provider = %q, want volcengine", trainer.provider)
	}
}

func TestCloneVoiceStatusDefaultsToVolcengineProvider(t *testing.T) {
	trainer := &cloneVoiceEngine{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Voices: map[voice.Provider]voice.Engine{
			voice.ProviderVolcengine: trainer,
		},
		DefaultVoice: testVoiceProvider,
		Logger:       slog.Default(),
	})

	result, err := rt.CloneVoiceStatus(context.Background(), app.VoiceCloneRequest{
		AppID:       "app",
		AccessToken: "token",
		ResourceID:  "seed-icl-2.0",
		SpeakerID:   "S_test",
		Language:    "ja",
	})
	if err != nil {
		t.Fatalf("CloneVoiceStatus() error = %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("Status = %q", result.Status)
	}
	if trainer.provider != string(voice.ProviderVolcengine) {
		t.Fatalf("provider = %q, want volcengine", trainer.provider)
	}
}

func TestSynthesizeVoiceUsesProvidedTextAndProfile(t *testing.T) {
	voiceEngine := &recordingVoiceEngine{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Voices: map[voice.Provider]voice.Engine{
			voice.ProviderVolcengine: voiceEngine,
		},
		DefaultVoice: testVoiceProvider,
		Logger:       slog.Default(),
	})

	result, err := rt.SynthesizeVoice(context.Background(), app.VoiceSynthesisRequest{
		Provider: string(voice.ProviderVolcengine),
		Text:     "わかりました。",
		Plan:     app.VoicePlan{VoiceID: "S_test", Style: "test", Speed: 1, Pitch: 1},
		Emotion:  "calm",
		Character: app.Character{
			ID:          "tutor",
			DisplayName: "Tutor",
			VoiceID:     "S_fallback",
		},
		Profile: app.VoiceProfile{
			VoiceID:   "S_test",
			MediaType: "mp3",
			Extra:     map[string]string{"resource_id": "seed-icl-2.0"},
		},
	})
	if err != nil {
		t.Fatalf("SynthesizeVoice() error = %v", err)
	}
	if result.Format != "mp3" {
		t.Fatalf("Format = %q, want mp3", result.Format)
	}
	if voiceEngine.lastText != "わかりました。" {
		t.Fatalf("voice text = %q", voiceEngine.lastText)
	}
	if voiceEngine.lastPlan.VoiceID != "S_test" {
		t.Fatalf("voice plan = %#v", voiceEngine.lastPlan)
	}
	if voiceEngine.lastProfile.Extra["resource_id"] != "seed-icl-2.0" {
		t.Fatalf("profile extra = %#v", voiceEngine.lastProfile.Extra)
	}
}

func TestSynthesizeVoiceUsesCacheForSameVoiceInput(t *testing.T) {
	voiceEngine := &recordingVoiceEngine{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Voices: map[voice.Provider]voice.Engine{
			voice.ProviderVolcengine: voiceEngine,
		},
		DefaultVoice: voice.ProviderVolcengine,
		Logger:       slog.Default(),
	})
	req := app.VoiceSynthesisRequest{
		Provider: string(voice.ProviderVolcengine),
		Text:     "同じ台詞です。",
		Plan:     app.VoicePlan{VoiceID: "S_test", Style: "calm", Speed: 1, Pitch: 1},
		Emotion:  "calm",
		Character: app.Character{
			ID:      "tutor",
			VoiceID: "S_test",
		},
		Profile: app.VoiceProfile{
			MediaType: "mp3",
			Extra:     map[string]string{"resource_id": "seed-icl-2.0"},
		},
	}

	first, err := rt.SynthesizeVoice(context.Background(), req)
	if err != nil {
		t.Fatalf("first SynthesizeVoice() error = %v", err)
	}
	second, err := rt.SynthesizeVoice(context.Background(), req)
	if err != nil {
		t.Fatalf("second SynthesizeVoice() error = %v", err)
	}
	if voiceEngine.calls != 1 {
		t.Fatalf("voice calls = %d, want 1", voiceEngine.calls)
	}
	if second.URL != first.URL {
		t.Fatalf("cached URL = %q, want %q", second.URL, first.URL)
	}
	if !second.Cached {
		t.Fatal("second result Cached = false, want true")
	}
}

func TestSynthesizeVoicePersistsWorkflowNodeAudio(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	voiceEngine := &recordingVoiceEngine{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: voiceEngine,
		},
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	const sessionID = "voice-history:default"
	const nodeID = "lesson-1"
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "语音缓存",
		Characters: []app.Character{{ID: "tutor", DisplayName: "Tutor", VoiceID: "mock"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "voice-history", Title: "语音缓存"},
		Session: app.Session{ID: sessionID, UserID: "default"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: nodeID,
			Nodes: []app.TeachingWorkflowNode{
				readyWorkflowNode(nodeID, "lesson", ""),
			},
		},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	audio, err := rt.SynthesizeVoice(context.Background(), app.VoiceSynthesisRequest{
		Provider:       string(testVoiceProvider),
		Text:           "同じ台詞です。",
		SessionID:      sessionID,
		WorkflowNodeID: nodeID,
		Plan:           app.VoicePlan{VoiceID: "mock", Style: "calm", Speed: 1, Pitch: 1},
		Character:      app.Character{ID: "tutor", VoiceID: "mock"},
		Profile:        app.VoiceProfile{MediaType: "mp3"},
	})
	if err != nil {
		t.Fatalf("SynthesizeVoice() error = %v", err)
	}
	record, err := rt.Session(sessionID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	for _, item := range record.Workflow.History {
		if item.NodeID == nodeID {
			if item.AudioURL != audio.URL {
				t.Fatalf("history audio_url = %q, want %q", item.AudioURL, audio.URL)
			}
			if item.AudioFormat != audio.Format {
				t.Fatalf("history audio_format = %q, want %q", item.AudioFormat, audio.Format)
			}
			event := runtimeEventByType(record.Events, app.RuntimeEventTypeVoiceSynthesizeDone)
			if event.ID == "" || event.Provider != string(testVoiceProvider) || event.NodeID != nodeID || event.Stage != app.RuntimeEventStageVoice || event.DurationMS <= 0 {
				t.Fatalf("voice.synthesize.completed event missing duration/provider/node: %+v", record.Events)
			}
			return
		}
	}
	t.Fatalf("workflow history missing node %s", nodeID)
}

func TestUpdateWorkflowNodePersistsReadyAudio(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	resp := app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:default", UserID: "default", ActiveCharacterID: "atri"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{{
				ID:      "opening",
				Kind:    "opening",
				Title:   "开场",
				Speaker: "亚托莉",
				Line:    "你好",
			}},
		},
	}
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, resp); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	readyNode := resp.Workflow.Nodes[0]
	readyNode.Status = app.WorkflowNodeStatusReady
	readyNode.VoiceStatus = app.WorkflowNodeStatusReady
	readyNode.Lines = []app.DialogueLine{{
		Speaker:     "亚托莉",
		Text:        "你好",
		SpeechText:  "こんにちは。",
		AudioStatus: app.DialogueAudioStatusReady,
		Audio:       app.AudioResult{URL: "/audio/opening.mp3", Format: "mp3"},
	}}
	if _, err := store.UpdateWorkflowNode("lesson:default", readyNode); err != nil {
		t.Fatalf("UpdateWorkflowNode() error = %v", err)
	}
	record, err := store.Get("lesson:default")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	got := record.Workflow.Nodes[0]
	if got.Status != app.WorkflowNodeStatusReady || got.Lines[0].Audio.URL != "/audio/opening.mp3" {
		t.Fatalf("persisted node = %+v", got)
	}
}

func TestFileSessionStoreGenerationLifecycle(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	request := app.SceneGenerateRequest{
		Topic:          "调度器",
		MaterialSource: textMaterial("GMP 调度模型"),
		LearningGoal:   "理解 GMP",
		Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		Runtime:        app.RuntimeConfig{SceneProvider: "mock"},
	}
	record := app.SessionRecord{
		Session: app.Session{
			ID:                "generation:one",
			UserID:            "default",
			ActiveCharacterID: "atri",
			ParticipantIDs:    []string{"atri"},
		},
		Scene:      app.Scene{ID: "generation:one", Title: "调度器"},
		Characters: request.Characters,
		Generation: app.SceneGeneration{
			Status:      app.SceneGenerationStatusGenerating,
			Fingerprint: "fp-1",
			Request:     request,
		},
	}
	if _, err := store.CreateGeneration(record); err != nil {
		t.Fatalf("CreateGeneration() error = %v", err)
	}
	found, ok, err := store.GenerationByFingerprint("fp-1")
	if err != nil {
		t.Fatalf("GenerationByFingerprint() error = %v", err)
	}
	if !ok || found.Session.ID != "generation:one" {
		t.Fatalf("GenerationByFingerprint() = (%q, %v), want generation:one true", found.Session.ID, ok)
	}
	generating, err := store.ListGeneration(app.SceneGenerationStatusGenerating)
	if err != nil {
		t.Fatalf("ListGeneration() error = %v", err)
	}
	if len(generating) != 1 {
		t.Fatalf("generating records = %d, want 1", len(generating))
	}

	resp := app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{UserID: "default", ActiveCharacterID: "atri"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{{
				ID:      "opening",
				Kind:    "opening",
				Title:   "开场",
				Speaker: "亚托莉",
				Line:    "你好",
			}},
		},
		OpeningMessage: "你好",
	}
	completed, err := store.CompleteGeneration("generation:one", resp)
	if err != nil {
		t.Fatalf("CompleteGeneration() error = %v", err)
	}
	if completed.Generation.Status != app.SceneGenerationStatusPreparing {
		t.Fatalf("completed status = %q, want preparing", completed.Generation.Status)
	}
	if completed.Session.ID != "generation:one" {
		t.Fatalf("completed session id = %q, want original generation id", completed.Session.ID)
	}
	if len(completed.Messages) != 1 || completed.Messages[0].Text != "你好" {
		t.Fatalf("completed opening message missing: %+v", completed.Messages)
	}
}

func TestFileSessionStoreGenerationFailureAndDelete(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	request := app.SceneGenerateRequest{
		Topic:      "调度器",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}
	_, err := store.CreateGeneration(app.SessionRecord{
		Session: app.Session{ID: "generation:fail", UserID: "default", ActiveCharacterID: "atri"},
		Scene:   app.Scene{ID: "generation:fail", Title: "调度器"},
		Generation: app.SceneGeneration{
			Status:      app.SceneGenerationStatusGenerating,
			Fingerprint: "fp-fail",
			Request:     request,
		},
	})
	if err != nil {
		t.Fatalf("CreateGeneration() error = %v", err)
	}
	failed, err := store.FailGeneration("generation:fail", "provider down")
	if err != nil {
		t.Fatalf("FailGeneration() error = %v", err)
	}
	if failed.Generation.Status != app.SceneGenerationStatusFailed || !strings.Contains(failed.Generation.Error, "provider down") {
		t.Fatalf("failed generation = %+v", failed.Generation)
	}
	if err := store.Delete("generation:fail"); err != nil {
		t.Fatalf("Delete(existing) error = %v", err)
	}
	if err := store.Delete("generation:missing"); err == nil {
		t.Fatal("Delete(missing) error = nil")
	}
}

func TestFileSessionStoreRuntimeEvents(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	request := app.SceneGenerateRequest{
		Topic:      "调度器",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}
	if _, err := store.BeginScene(request, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:events", UserID: "default", ActiveCharacterID: "atri"},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	later := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Minute)
	if _, err := store.AppendEvent("lesson:events", app.RuntimeEvent{
		Level:      app.RuntimeEventLevelWarn,
		Type:       "workflow.waiting",
		Stage:      app.RuntimeEventStageWorkflow,
		Message:    "下一幕还在生成",
		NodeID:     "lesson-1",
		DurationMS: 1250,
		CreatedAt:  later,
	}); err != nil {
		t.Fatalf("AppendEvent(later) error = %v", err)
	}
	if _, err := store.AppendEvent("lesson:events", app.RuntimeEvent{
		Level:     app.RuntimeEventLevelError,
		Type:      app.RuntimeEventTypeWorkflowNodeFailed,
		Stage:     app.RuntimeEventStageWorkflow,
		Message:   "节点生成失败",
		NodeID:    "lesson-0",
		CreatedAt: earlier,
	}); err != nil {
		t.Fatalf("AppendEvent(earlier) error = %v", err)
	}
	events, err := store.SessionEvents("lesson:events")
	if err != nil {
		t.Fatalf("SessionEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Type != app.RuntimeEventTypeWorkflowNodeFailed || events[1].Type != "workflow.waiting" {
		t.Fatalf("events order = %#v", events)
	}
	if events[0].ID == "" || events[0].SessionID != "lesson:events" {
		t.Fatalf("normalized event = %+v", events[0])
	}
	if events[1].DurationMS != 1250 {
		t.Fatalf("duration_ms = %d, want 1250", events[1].DurationMS)
	}
	events[0].Message = "mutated"
	again, err := store.SessionEvents("lesson:events")
	if err != nil {
		t.Fatalf("SessionEvents(second) error = %v", err)
	}
	if again[0].Message == "mutated" {
		t.Fatal("SessionEvents returned mutable backing slice")
	}

	for index := 0; index < 205; index++ {
		if _, err := store.AppendEvent("lesson:events", app.RuntimeEvent{
			Level:     app.RuntimeEventLevelInfo,
			Type:      app.RuntimeEventTypeGenerationCreated,
			Stage:     app.RuntimeEventStageGeneration,
			Message:   fmt.Sprintf("事件 %03d", index),
			CreatedAt: later.Add(time.Duration(index+1) * time.Second),
		}); err != nil {
			t.Fatalf("AppendEvent(%d) error = %v", index, err)
		}
	}
	capped, err := store.SessionEvents("lesson:events")
	if err != nil {
		t.Fatalf("SessionEvents(capped) error = %v", err)
	}
	if len(capped) != 200 {
		t.Fatalf("capped events = %d, want 200", len(capped))
	}
	if capped[0].Message != "事件 005" {
		t.Fatalf("first capped event = %q, want 事件 005", capped[0].Message)
	}
}

func TestFileSessionStoreCompleteGenerationPreservesRuntimeEvents(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	request := app.SceneGenerateRequest{
		Topic:      "调度器",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}
	_, err := store.CreateGeneration(app.SessionRecord{
		Session: app.Session{ID: "generation:events", UserID: "default", ActiveCharacterID: "atri"},
		Scene:   app.Scene{ID: "generation:events", Title: "调度器"},
		Generation: app.SceneGeneration{
			Status:      app.SceneGenerationStatusGenerating,
			Fingerprint: "fp-events",
			Request:     request,
		},
	})
	if err != nil {
		t.Fatalf("CreateGeneration() error = %v", err)
	}
	if _, err := store.AppendEvent("generation:events", app.RuntimeEvent{
		Level:   app.RuntimeEventLevelInfo,
		Type:    app.RuntimeEventTypeGenerationCreated,
		Stage:   app.RuntimeEventStageGeneration,
		Message: "生成任务已创建",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	completed, err := store.CompleteGeneration("generation:events", app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{UserID: "default", ActiveCharacterID: "atri"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{{
				ID:      "opening",
				Kind:    "opening",
				Title:   "开场",
				Speaker: "亚托莉",
				Line:    "你好",
			}},
		},
	})
	if err != nil {
		t.Fatalf("CompleteGeneration() error = %v", err)
	}
	if len(completed.Events) != 1 || completed.Events[0].Type != app.RuntimeEventTypeGenerationCreated {
		t.Fatalf("completed events = %+v", completed.Events)
	}
}

func TestStartSceneGenerationCreatesPendingAndDeduplicates(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	started := make(chan struct{})
	release := make(chan struct{})
	agentEngine := &blockingGenerationAgent{started: started, release: release}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	request := asyncSceneRequest()
	first, err := rt.StartSceneGeneration(context.Background(), request)
	if err != nil {
		t.Fatalf("StartSceneGeneration() error = %v", err)
	}
	waitForSignal(t, started, "scene generation started")
	if first.Record.Generation.Status != app.SceneGenerationStatusGenerating {
		t.Fatalf("first status = %q, want generating", first.Record.Generation.Status)
	}
	second, err := rt.StartSceneGeneration(context.Background(), request)
	if err != nil {
		t.Fatalf("StartSceneGeneration(duplicate) error = %v", err)
	}
	if !second.Duplicate || second.Record.Session.ID != first.Record.Session.ID {
		t.Fatalf("duplicate = %v session=%q, want same %q", second.Duplicate, second.Record.Session.ID, first.Record.Session.ID)
	}
	close(release)
	record := waitForGenerationStatusIn(t, store, first.Record.Session.ID, app.SceneGenerationStatusPreparing, app.SceneGenerationStatusReady)
	if record.Scene.ID == "" || len(record.Workflow.Nodes) == 0 {
		t.Fatalf("preparing record missing scene/workflow: %+v", record)
	}
	if event := runtimeEventByType(record.Events, app.RuntimeEventTypeGenerationCreated); event.ID == "" {
		t.Fatalf("generation.created event missing: %+v", record.Events)
	}
	if event := runtimeEventByType(record.Events, app.RuntimeEventTypeMaterialPrepared); event.ID == "" || event.DurationMS <= 0 {
		t.Fatalf("material.prepared event missing duration: %+v", record.Events)
	}
	if event := runtimeEventByType(record.Events, app.RuntimeEventTypeGenerationComplete); event.ID == "" {
		t.Fatalf("generation.completed event missing: %+v", record.Events)
	} else if event.DurationMS <= 0 {
		t.Fatalf("generation.completed duration_ms = %d, want > 0", event.DurationMS)
	}
}

func TestStartSceneGenerationPersistsOpeningAgentSubstepEventsOnGenerationSession(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: &tracingGenerationAgent{}},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	first, err := rt.StartSceneGeneration(context.Background(), asyncSceneRequest())
	if err != nil {
		t.Fatalf("StartSceneGeneration() error = %v", err)
	}

	record := waitForGenerationStatusIn(t, store, first.Record.Session.ID, app.SceneGenerationStatusPreparing, app.SceneGenerationStatusReady)
	event := runtimeEventByType(record.Events, app.RuntimeEventTypeAgentActPlanDone)
	if event.ID == "" {
		t.Fatalf("agent.actplan.completed event missing: %+v", record.Events)
	}
	if event.SessionID != first.Record.Session.ID {
		t.Fatalf("agent actplan event session_id = %q, want generation session %q", event.SessionID, first.Record.Session.ID)
	}
	if event.NodeID != "opening" {
		t.Fatalf("agent actplan event node_id = %q, want opening", event.NodeID)
	}
	if event.Provider != string(agent.ProviderMock) {
		t.Fatalf("agent actplan event provider = %q, want %q", event.Provider, agent.ProviderMock)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("agent actplan event duration_ms = %d, want > 0", event.DurationMS)
	}
}

func TestStartSceneGenerationKeepsPreparingUntilWorkflowCompleteAndDeduplicates(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	agentEngine := &controlledGenerationAgent{
		secondStarted: make(chan struct{}),
		secondRelease: make(chan struct{}),
	}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		Scenes:       map[scene.Provider]scene.Engine{scene.ProviderMock: scenemock.Engine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		DefaultScene: scene.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	request := asyncSceneRequest()
	first, err := rt.StartSceneGeneration(context.Background(), request)
	if err != nil {
		t.Fatalf("StartSceneGeneration() error = %v", err)
	}
	waitForSignal(t, agentEngine.secondStarted, "followup generation started")
	preparing, err := store.Get(first.Record.Session.ID)
	if err != nil {
		t.Fatalf("Get(preparing) error = %v", err)
	}
	if preparing.Generation.Status != app.SceneGenerationStatusPreparing {
		t.Fatalf("generation status after opening = %q, want preparing", preparing.Generation.Status)
	}
	duplicate, err := rt.StartSceneGeneration(context.Background(), request)
	if err != nil {
		t.Fatalf("StartSceneGeneration(duplicate while preparing) error = %v", err)
	}
	if !duplicate.Duplicate || duplicate.Record.Session.ID != first.Record.Session.ID {
		t.Fatalf("duplicate while preparing = %v session=%q, want same %q", duplicate.Duplicate, duplicate.Record.Session.ID, first.Record.Session.ID)
	}

	close(agentEngine.secondRelease)
	ready := waitForGenerationStatus(t, store, first.Record.Session.ID, app.SceneGenerationStatusReady)
	if workflowNodeByID(ready.Workflow.Nodes, "lesson-1").Status != app.WorkflowNodeStatusReady {
		t.Fatalf("lesson-1 not ready after generation completes: %+v", ready.Workflow.Nodes)
	}
	if workflowNodeByID(ready.Workflow.Nodes, "free-discussion").Kind != "free_discussion" {
		t.Fatalf("free discussion terminal node missing: %+v", ready.Workflow.Nodes)
	}
	if calls := agentEngine.callCount(); calls < 3 {
		t.Fatalf("agent calls = %d, want at least opening + lesson + summary", calls)
	}
}

func TestStartSceneGenerationStoresFailure(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: failingGenerationAgent{err: errors.New("agent provider down")}},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	resp, err := rt.StartSceneGeneration(context.Background(), asyncSceneRequest())
	if err != nil {
		t.Fatalf("StartSceneGeneration() error = %v", err)
	}
	record := waitForGenerationStatus(t, store, resp.Record.Session.ID, app.SceneGenerationStatusFailed)
	if !strings.Contains(record.Generation.Error, "agent provider down") {
		t.Fatalf("generation error = %q", record.Generation.Error)
	}
	event := runtimeEventByType(record.Events, app.RuntimeEventTypeGenerationFailed)
	if event.ID == "" {
		t.Fatalf("generation.failed event missing: %+v", record.Events)
	}
	if event.Level != app.RuntimeEventLevelError || event.Stage != app.RuntimeEventStageGeneration || !strings.Contains(event.Message, "agent provider down") {
		t.Fatalf("generation.failed event = %+v", event)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("generation.failed duration_ms = %d, want > 0", event.DurationMS)
	}
}

func TestRuntimeResumesGeneratingTasksOnStartup(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	request := asyncSceneRequest()
	pending := app.SessionRecord{
		Session: app.Session{ID: "generation:resume", UserID: "default", ActiveCharacterID: "atri", ParticipantIDs: []string{"atri"}},
		Scene:   app.Scene{ID: "generation:resume", Title: "恢复生成"},
		Generation: app.SceneGeneration{
			Status:      app.SceneGenerationStatusGenerating,
			Fingerprint: "resume-fp",
			Request:     request,
		},
	}
	if _, err := store.CreateGeneration(pending); err != nil {
		t.Fatalf("CreateGeneration() error = %v", err)
	}
	_ = runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentmock.MockEngine{}},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		Scenes:       map[scene.Provider]scene.Engine{scene.ProviderMock: scenemock.Engine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		DefaultScene: scene.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	record := waitForGenerationStatus(t, store, "generation:resume", app.SceneGenerationStatusPreparing)
	if record.Session.ID != "generation:resume" {
		t.Fatalf("resumed session id = %q", record.Session.ID)
	}
}

func TestRuntimeResumesWorkflowFollowupsOnStartup(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	request := asyncSceneRequest()
	request.Topic = "节点恢复"
	request.MaterialSource = textMaterial("第一幕完成后，第二幕必须在重启后继续生成。")
	request.LearningGoal = "理解节点级恢复。"
	_, err := store.BeginScene(request, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "节点恢复"},
		Session: app.Session{ID: "lesson:resume", UserID: "default", ActiveCharacterID: "atri"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Preparing:     true,
			PendingNodeID: "lesson-1",
			Nodes: []app.TeachingWorkflowNode{
				readyWorkflowNode("opening", "opening", "lesson-1"),
			},
		},
	})
	if err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	_ = runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentmock.MockEngine{}},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	record := waitForStoredWorkflowNodeReady(t, store, "lesson:resume", "lesson-1")
	if record.Workflow.Preparing || record.Workflow.PendingNodeID != "" {
		t.Fatalf("workflow preparing state = %v/%q, want cleared", record.Workflow.Preparing, record.Workflow.PendingNodeID)
	}
}

func TestRuntimeResumesMissingChoiceBranchOnStartup(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	request := asyncSceneRequest()
	request.Topic = "分支恢复"
	request.MaterialSource = textMaterial("选项分支必须在重启后补齐。")
	request.LearningGoal = "理解分支预加载。"
	opening := readyWorkflowNode("opening", "opening", "lesson-1")
	opening.Choices = []app.SceneChoice{{
		ID:           "example",
		Label:        "先看例子",
		Text:         "先用例子继续。",
		TargetNodeID: "opening-choice-example",
	}}
	_, err := store.BeginScene(request, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "分支恢复"},
		Session: app.Session{ID: "lesson:branch-resume", UserID: "default", ActiveCharacterID: "atri"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{
				opening,
				readyWorkflowNode("lesson-1", "lesson", ""),
			},
		},
	})
	if err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	_ = runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentmock.MockEngine{}},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	record := waitForStoredWorkflowNodeReady(t, store, "lesson:branch-resume", "opening-choice-example")
	branch := workflowNodeByID(record.Workflow.Nodes, "opening-choice-example")
	if branch.Kind != "choice" {
		t.Fatalf("branch kind = %q, want choice", branch.Kind)
	}
}

func TestSessionStoreNormalizesLegacyWorkflowAudio(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	state := map[string]app.SessionRecord{
		"lesson:default": {
			Session: app.Session{ID: "lesson:default", UserID: "default"},
			Scene:   app.Scene{ID: "lesson", Title: "课程"},
			Workflow: app.TeachingWorkflow{
				ID:            "wf",
				CurrentNodeID: "opening",
				Nodes: []app.TeachingWorkflowNode{{
					ID:         "opening",
					Kind:       "opening",
					Title:      "开场",
					Speaker:    "亚托莉",
					Line:       "你好",
					SpeechText: "こんにちは。",
				}},
				History: []app.WorkflowHistoryItem{{
					NodeID:      "opening",
					AudioURL:    "/audio/legacy.mp3",
					AudioFormat: "mp3",
				}},
			},
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	record, err := runtime.NewFileSessionStore(path).Get("lesson:default")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	node := record.Workflow.Nodes[0]
	if node.Status != app.WorkflowNodeStatusReady || len(node.Lines) != 1 {
		t.Fatalf("legacy node not normalized: %+v", node)
	}
	if node.Lines[0].Audio.URL != "/audio/legacy.mp3" {
		t.Fatalf("legacy audio url = %q", node.Lines[0].Audio.URL)
	}
}

func TestSynthesizeVoiceRequiresText(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultVoice: testVoiceProvider,
		Logger:       slog.Default(),
	})

	_, err := rt.SynthesizeVoice(context.Background(), app.VoiceSynthesisRequest{
		Provider: string(testVoiceProvider),
	})
	if err == nil {
		t.Fatal("SynthesizeVoice() error = nil, want text error")
	}
}

func TestSynthesizeVoiceRecordsRuntimeEventOnProviderFailure(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "语音失败",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:voice-failure", UserID: "default", ActiveCharacterID: "atri"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes:         []app.TeachingWorkflowNode{readyWorkflowNode("opening", "opening", "")},
		},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: failingVoiceEngine{err: errors.New("voice provider down")}},
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	_, err := rt.SynthesizeVoice(context.Background(), app.VoiceSynthesisRequest{
		Provider:       string(testVoiceProvider),
		Text:           "こんにちは",
		SessionID:      "lesson:voice-failure",
		WorkflowNodeID: "opening",
		Character:      app.Character{ID: "atri", DisplayName: "亚托莉"},
		Plan:           app.VoicePlan{VoiceID: "atri"},
	})
	if err == nil {
		t.Fatal("SynthesizeVoice() error = nil, want provider error")
	}
	record, err := store.Get("lesson:voice-failure")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	event := runtimeEventByType(record.Events, app.RuntimeEventTypeVoiceSynthesizeFailed)
	if event.ID == "" {
		t.Fatalf("voice.synthesize.failed event missing: %+v", record.Events)
	}
	if event.Provider != string(testVoiceProvider) || event.NodeID != "opening" || event.Stage != app.RuntimeEventStageVoice || !strings.Contains(event.Message, "voice provider down") {
		t.Fatalf("voice.synthesize.failed event = %+v", event)
	}
	if event.RetryCount != 0 {
		t.Fatalf("retry count = %d, want 0", event.RetryCount)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("voice.synthesize.failed duration_ms = %d, want > 0", event.DurationMS)
	}
}

func TestTurnRejectsWorkflowContextLeak(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: leakingAgent{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		Images: map[image.Provider]image.Engine{
			image.ProviderMock: imagemock.Engine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		Logger:       slog.Default(),
	})

	resp, err := rt.Turn(context.Background(), app.TurnRequest{
		Character: app.Character{ID: "tutor", DisplayName: "Tutor", VoiceID: "mock"},
		Scene:     app.Scene{ID: "lesson", Title: "测试课程"},
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
			ImageProvider: string(image.ProviderMock),
		},
		User: app.UserInput{UserID: "default", Text: "你是谁？"},
	})
	if err != nil {
		t.Fatalf("Turn() error = %v", err)
	}
	if strings.Contains(resp.DisplayText, "OpenSpec") || strings.Contains(resp.DisplayText, "Superpowers") {
		t.Fatalf("DisplayText still leaks workflow context: %q", resp.DisplayText)
	}
	if len(resp.Segments) == 0 || strings.Contains(resp.Segments[0].Text, "OpenSpec") {
		t.Fatalf("Segments were not sanitized: %#v", resp.Segments)
	}
}

func TestTurnKeepsSpeechWhenSceneImageFails(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		Images: map[image.Provider]image.Engine{
			image.ProviderMock: failingImageEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		DefaultScene: scene.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	character := app.Character{ID: "tutor", DisplayName: "Tutor", VoiceID: "mock", Persona: "负责讲解文档概念"}
	req := app.TurnRequest{
		Session: app.Session{
			ID:                "lesson:test",
			UserID:            "default",
			ActiveCharacterID: "tutor",
		},
		Characters: []app.Character{character},
		Scene:      app.Scene{ID: "lesson", Title: "测试课程"},
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
			ImageProvider: string(image.ProviderMock),
			Image: app.ImageRequest{
				Enabled: true,
				Prompt:  "broken cg",
			},
		},
		User: app.UserInput{
			UserID: "default",
			Text:   "继续解释一下。",
		},
	}
	resp, err := rt.Turn(context.Background(), req)
	if err != nil {
		t.Fatalf("Turn() error = %v", err)
	}
	if resp.DisplayText == "" {
		t.Fatal("Turn() display_text is empty")
	}
	if resp.SceneImage.Error == "" {
		t.Fatal("Turn() scene image error is empty")
	}

	record, err := rt.Session("lesson:test")
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if len(record.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(record.Messages))
	}
	if record.Messages[1].SceneImageError == "" {
		t.Fatal("persisted scene image error is empty")
	}
	imageEvent := runtimeEventByType(record.Events, app.RuntimeEventTypeImageGenerateFailed)
	if imageEvent.ID == "" || imageEvent.Stage != app.RuntimeEventStageImage || imageEvent.Provider != string(image.ProviderMock) || !strings.Contains(imageEvent.Detail, "image provider unavailable") {
		t.Fatalf("image.generate.failed event = %+v", imageEvent)
	}
	if imageEvent.DurationMS <= 0 {
		t.Fatalf("image.generate.failed duration_ms = %d, want > 0", imageEvent.DurationMS)
	}
	persistEvent := runtimeEventByType(record.Events, app.RuntimeEventTypePersistTurnSaved)
	if persistEvent.ID == "" || persistEvent.Stage != app.RuntimeEventStagePersist || persistEvent.DurationMS <= 0 {
		t.Fatalf("persist.turn.saved event = %+v", persistEvent)
	}
}

func TestTurnSkipsImageRuntimeEventWhenImageDisabled(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: &recordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	req := app.TurnRequest{
		Session: app.Session{
			ID:                "lesson:no-image",
			UserID:            "default",
			ActiveCharacterID: "tutor",
		},
		Characters: []app.Character{{ID: "tutor", DisplayName: "Tutor", VoiceID: "mock"}},
		Scene:      app.Scene{ID: "lesson", Title: "测试课程"},
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
		},
		User: app.UserInput{UserID: "default", Text: "继续。"},
	}
	if _, err := rt.Turn(context.Background(), req); err != nil {
		t.Fatalf("Turn() error = %v", err)
	}
	record, err := rt.Session("lesson:no-image")
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if event := runtimeEventByType(record.Events, app.RuntimeEventTypeImageGenerateDone); event.ID != "" {
		t.Fatalf("unexpected image.generate.completed event = %+v", event)
	}
	if event := runtimeEventByType(record.Events, app.RuntimeEventTypeImageGenerateFailed); event.ID != "" {
		t.Fatalf("unexpected image.generate.failed event = %+v", event)
	}
	persistEvent := runtimeEventByType(record.Events, app.RuntimeEventTypePersistTurnSaved)
	if persistEvent.ID == "" || persistEvent.Stage != app.RuntimeEventStagePersist || persistEvent.DurationMS <= 0 {
		t.Fatalf("persist.turn.saved event = %+v", persistEvent)
	}
}

func TestTurnUsesRuntimeProvidersFromActiveCharacterResource(t *testing.T) {
	agentEngine := &recordingAgentEngine{}
	voiceEngine := &recordingVoiceEngine{}
	imageEngine := &recordingImageEngine{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.Provider("role-agent"): agentEngine,
		},
		Voices: map[voice.Provider]voice.Engine{
			voice.Provider("role-voice"): voiceEngine,
		},
		Images: map[image.Provider]image.Engine{
			image.Provider("role-image"): imageEngine,
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		Logger:       slog.Default(),
	})

	character := app.Character{
		ID:          "fairy",
		DisplayName: "亚托莉",
		VoiceID:     "S_fairy",
		Runtime: app.RuntimeConfig{
			AgentProvider: "role-agent",
			VoiceProvider: "role-voice",
			ImageProvider: "role-image",
		},
	}
	req := app.TurnRequest{
		Session:    app.Session{ID: "lesson:role-resource", UserID: "default", ActiveCharacterID: character.ID},
		Characters: []app.Character{character},
		Character:  character,
		Scene:      app.Scene{ID: "lesson", Title: "角色资源测试"},
		Runtime: app.RuntimeConfig{
			AgentProvider: character.Runtime.AgentProvider,
			VoiceProvider: character.Runtime.VoiceProvider,
			ImageProvider: character.Runtime.ImageProvider,
			Image: app.ImageRequest{
				Enabled: true,
				Prompt:  "role controlled cg",
			},
		},
		User: app.UserInput{UserID: "default", Text: "解释一下这段材料。"},
	}
	resp, err := rt.Turn(context.Background(), req)
	if err != nil {
		t.Fatalf("Turn() error = %v", err)
	}
	if !agentEngine.called {
		t.Fatal("role agent provider was not called")
	}
	if voiceEngine.lastText != resp.SpeechText {
		t.Fatalf("voice text = %q, want response speech_text %q", voiceEngine.lastText, resp.SpeechText)
	}
	if voiceEngine.lastProfile.VoiceID != "" {
		t.Fatalf("unexpected voice profile = %#v", voiceEngine.lastProfile)
	}
	if imageEngine.lastPrompt != "role controlled cg" {
		t.Fatalf("image prompt = %q", imageEngine.lastPrompt)
	}
	if imageEngine.lastCharacter.ID != "fairy" {
		t.Fatalf("image character = %q, want fairy", imageEngine.lastCharacter.ID)
	}
}

func TestTurnUsesDiscussWithWorkflowContext(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	agentEngine := &discussionRecordingAgent{}
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	sceneResp := app.SceneGenerateResponse{
		Scene:    app.Scene{ID: "lesson", Title: "Go 调度器"},
		Session:  app.Session{ID: "lesson:default", UserID: "default", ActiveCharacterID: "atri", ParticipantIDs: []string{"atri"}},
		Relation: app.Relationship{UserID: "default"},
		Workflow: app.TeachingWorkflow{
			CurrentNodeID: "free-discussion",
			Nodes: []app.TeachingWorkflowNode{
				{ID: "summary", Kind: "summary", Title: "总结回收", Summary: "已经讲完 GMP 主线", NextNodeID: "free-discussion", Status: app.WorkflowNodeStatusReady, VoiceStatus: app.WorkflowNodeStatusReady},
				{ID: "free-discussion", Kind: "free_discussion", Title: "自由讨论", Summary: "围绕材料继续问答", FreeDiscussion: true, Status: app.WorkflowNodeStatusReady, VoiceStatus: app.WorkflowNodeStatusReady},
			},
			History: []app.WorkflowHistoryItem{{NodeID: "summary", NodeTitle: "总结回收"}},
		},
	}
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:          "Go 调度器",
		MaterialSource: textMaterial("GMP 调度材料"),
		LearningGoal:   "理解 GMP 分工",
		Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "mock"}},
		MaterialContext: app.MaterialContext{
			Brief:  "GMP 包含 G、M、P 三个角色。",
			Report: app.MaterialSourceReport{Summary: "材料摘要：GMP 调度"},
		},
	}, sceneResp); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}

	if _, err := rt.Turn(context.Background(), app.TurnRequest{
		Session:   sceneResp.Session,
		Character: app.Character{ID: "atri", DisplayName: "亚托莉", VoiceID: "mock"},
		Scene:     sceneResp.Scene,
		User:      app.UserInput{UserID: "default", Text: "P 为什么重要？"},
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
		},
	}); err != nil {
		t.Fatalf("Turn() error = %v", err)
	}
	if agentEngine.generateCalls != 0 {
		t.Fatalf("GenerateAct calls = %d, want 0", agentEngine.generateCalls)
	}
	if agentEngine.discussCalls != 1 {
		t.Fatalf("Discuss calls = %d, want 1", agentEngine.discussCalls)
	}
	input := agentEngine.lastDiscuss
	if input.CurrentNode.ID != "free-discussion" || input.Workflow.CurrentNodeID != "free-discussion" {
		t.Fatalf("Discuss workflow context = node:%q current:%q", input.CurrentNode.ID, input.Workflow.CurrentNodeID)
	}
	if !strings.Contains(input.MaterialSummary, "GMP") {
		t.Fatalf("MaterialSummary = %q, want GMP summary", input.MaterialSummary)
	}
	if !strings.Contains(input.SessionSummary, "Go 调度器") || !strings.Contains(input.SessionSummary, "自由讨论") {
		t.Fatalf("SessionSummary = %q, want topic and current node", input.SessionSummary)
	}
}

type failingImageEngine struct{}

func (failingImageEngine) Generate(context.Context, image.Input) (app.ImageResult, error) {
	return app.ImageResult{}, errors.New("image provider unavailable")
}

type leakingAgent struct{}

func (leakingAgent) GenerateAct(context.Context, agent.ActInput) (agent.ActOutput, error) {
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
			ID:      "opening",
			Kind:    "opening",
			Title:   "开场",
			Summary: "summary",
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "第一句", SpeechText: "第一句", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "第二句", SpeechText: "第二句", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "第三句", SpeechText: "第三句", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "第四句", SpeechText: "第四句", Expression: "calm"},
			},
		},
	}, nil
}

func (leakingAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{
		DisplayText: "我是 OpenSpec 流程讲解者。",
		SpeechText:  "OpenSpec と Superpowers を説明します。",
		Emotion:     "calm",
		Expression:  "soft_smile",
		Motion:      "idle",
		Voice:       app.VoicePlan{VoiceID: "mock", Style: "natural", Speed: 1, Pitch: 1},
	}, nil
}

type scaffoldlessAgent struct{}

func (scaffoldlessAgent) GenerateAct(context.Context, agent.ActInput) (agent.ActOutput, error) {
	return agent.ActOutput{
		Node: app.TeachingWorkflowNode{
			Summary: "GMP 入门",
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "第一句", SpeechText: "一つ目", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "第二句", SpeechText: "二つ目", Expression: "thinking"},
				{Speaker: "亚托莉", Text: "第三句", SpeechText: "三つ目", Expression: "curious"},
				{Speaker: "亚托莉", Text: "第四句", SpeechText: "四つ目", Expression: "calm"},
			},
			Choices: testSceneChoices(),
		},
	}, nil
}

func (scaffoldlessAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{
		DisplayText: "我们继续。",
		SpeechText:  "続けましょう。",
		Expression:  "soft_smile",
		Motion:      "idle",
	}, nil
}

type bilingualAgent struct{}

func (bilingualAgent) GenerateAct(context.Context, agent.ActInput) (agent.ActOutput, error) {
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
			ID:      "opening",
			Kind:    "opening",
			Title:   "开场",
			Summary: "summary",
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "第一句", SpeechText: "一つ目", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "第二句", SpeechText: "二つ目", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "第三句", SpeechText: "三つ目", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "第四句", SpeechText: "四つ目", Expression: "calm"},
			},
		},
	}, nil
}

func (bilingualAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{
		DisplayText: "注意力会让模型关注更重要的信息。",
		SpeechText:  "注意は大切な情報に目を向ける仕組みです。",
		Emotion:     "calm",
		Expression:  "soft_smile",
		Motion:      "idle",
		Voice:       app.VoicePlan{VoiceID: "S_test", Style: "natural", Speed: 1, Pitch: 1},
	}, nil
}

type recordingAgentEngine struct {
	called bool
}

func (e *recordingAgentEngine) GenerateAct(_ context.Context, input agent.ActInput) (agent.ActOutput, error) {
	e.called = true
	character := app.Character{}
	if len(input.Request.Characters) > 0 {
		character = input.Request.Characters[0]
	}
	node := app.TeachingWorkflowNode{
		ID:      "opening",
		Kind:    "opening",
		Title:   "开场",
		Summary: "角色资源",
		Speaker: character.DisplayName,
		Lines: []app.DialogueLine{
			{Speaker: character.DisplayName, Text: "我们按角色资源来解释。", SpeechText: "角色資源に従って説明します。", Expression: "soft_smile"},
			{Speaker: character.DisplayName, Text: "第二句。", SpeechText: "二つ目。", Expression: "soft_smile"},
			{Speaker: character.DisplayName, Text: "第三句。", SpeechText: "三つ目。", Expression: "soft_smile"},
			{Speaker: character.DisplayName, Text: "第四句。", SpeechText: "四つ目。", Expression: "calm"},
		},
		Choices: testSceneChoices(),
	}
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node:     node,
	}, nil
}

func (e *recordingAgentEngine) Discuss(_ context.Context, input agent.DiscussInput) (agent.Output, error) {
	e.called = true
	return agent.Output{
		DisplayText: "我们按角色资源来解释。",
		SpeechText:  "角色资源に従って説明します。",
		Emotion:     "calm",
		Expression:  "soft_smile",
		Motion:      "idle",
		Voice:       app.VoicePlan{VoiceID: input.Turn.Character.VoiceID, Style: "clear", Speed: 1, Pitch: 1},
	}, nil
}

type discussionRecordingAgent struct {
	generateCalls int
	discussCalls  int
	lastDiscuss   agent.DiscussInput
}

func (e *discussionRecordingAgent) GenerateAct(context.Context, agent.ActInput) (agent.ActOutput, error) {
	e.generateCalls++
	return agent.ActOutput{}, errors.New("GenerateAct should not be called during free discussion")
}

func (e *discussionRecordingAgent) Discuss(_ context.Context, input agent.DiscussInput) (agent.Output, error) {
	e.discussCalls++
	e.lastDiscuss = input
	return agent.Output{
		DisplayText: "P 保存本地可运行队列，所以会影响调度效率。",
		SpeechText:  "P は実行可能なキューを持つので、調度効率に関わります。",
		Emotion:     "calm",
		Expression:  "soft_smile",
		Motion:      "idle",
		Voice:       app.VoicePlan{VoiceID: input.Turn.Character.VoiceID, Style: "clear", Speed: 1, Pitch: 1},
	}, nil
}

type controlledGenerationAgent struct {
	calls         atomic.Int32
	secondStarted chan struct{}
	secondRelease chan struct{}
	secondOnce    atomic.Bool
}

func (e *controlledGenerationAgent) GenerateAct(ctx context.Context, input agent.ActInput) (agent.ActOutput, error) {
	call := e.calls.Add(1)
	speaker := "亚托莉"
	if len(input.Request.Characters) > 0 {
		speaker = input.Request.Characters[0].DisplayName
	}
	if call == 2 {
		if e.secondOnce.CompareAndSwap(false, true) {
			close(e.secondStarted)
		}
		select {
		case <-ctx.Done():
			return agent.ActOutput{}, ctx.Err()
		case <-e.secondRelease:
		}
		return generatedTestAct(input.PlannedNode, speaker, agent.ActDecisionSummarize), nil
	}
	if input.PlannedNode.Kind == "summary" {
		return generatedTestAct(input.PlannedNode, speaker, agent.ActDecisionFreeDiscussion), nil
	}
	if input.PlannedNode.Kind == "lesson" || input.PlannedNode.Kind == "choice" {
		return generatedTestAct(input.PlannedNode, speaker, agent.ActDecisionSummarize), nil
	}
	return generatedTestAct(input.PlannedNode, speaker, agent.ActDecisionContinue), nil
}

func (e *controlledGenerationAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{
		DisplayText: "继续自由讨论。",
		SpeechText:  "自由に話しましょう。",
		Emotion:     "calm",
		Expression:  "soft_smile",
		Motion:      "idle",
	}, nil
}

func (e *controlledGenerationAgent) callCount() int {
	return int(e.calls.Load())
}

type blockingGenerationAgent struct {
	calls   atomic.Int32
	started chan<- struct{}
	release <-chan struct{}
}

func (e *blockingGenerationAgent) GenerateAct(ctx context.Context, input agent.ActInput) (agent.ActOutput, error) {
	call := e.calls.Add(1)
	if call == 1 {
		if e.started != nil {
			select {
			case e.started <- struct{}{}:
			default:
			}
		}
		if e.release != nil {
			select {
			case <-e.release:
			case <-ctx.Done():
				return agent.ActOutput{}, ctx.Err()
			}
		}
	}
	speaker := "亚托莉"
	if len(input.Request.Characters) > 0 {
		speaker = input.Request.Characters[0].DisplayName
	}
	return generatedTestAct(input.PlannedNode, speaker, agent.ActDecisionContinue), nil
}

func (e *blockingGenerationAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{
		DisplayText: "继续自由讨论。",
		SpeechText:  "自由に話しましょう。",
		Emotion:     "calm",
		Expression:  "soft_smile",
		Motion:      "idle",
	}, nil
}

type tracingGenerationAgent struct{}

func (e *tracingGenerationAgent) GenerateAct(_ context.Context, input agent.ActInput) (agent.ActOutput, error) {
	if input.Trace != nil {
		input.Trace(agent.ActTraceEvent{
			Type:       app.RuntimeEventTypeAgentActPlanDone,
			Level:      app.RuntimeEventLevelInfo,
			Step:       agent.ActTraceStepActPlan,
			Message:    "ActPlan 已生成。",
			DurationMS: 5,
		})
	}
	speaker := "亚托莉"
	if len(input.Request.Characters) > 0 {
		speaker = input.Request.Characters[0].DisplayName
	}
	decision := agent.ActDecisionContinue
	if input.PlannedNode.Kind == "summary" {
		decision = agent.ActDecisionFreeDiscussion
	} else if input.PlannedNode.Kind == "lesson" || input.PlannedNode.Kind == "choice" {
		decision = agent.ActDecisionSummarize
	}
	return generatedTestAct(input.PlannedNode, speaker, decision), nil
}

func (e *tracingGenerationAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{
		DisplayText: "继续自由讨论。",
		SpeechText:  "自由に話しましょう。",
		Emotion:     "calm",
		Expression:  "soft_smile",
		Motion:      "idle",
	}, nil
}

type failingGenerationAgent struct {
	err error
}

func (e failingGenerationAgent) GenerateAct(context.Context, agent.ActInput) (agent.ActOutput, error) {
	if e.err != nil {
		return agent.ActOutput{}, e.err
	}
	return agent.ActOutput{}, errors.New("agent provider failed")
}

func (e failingGenerationAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	if e.err != nil {
		return agent.Output{}, e.err
	}
	return agent.Output{}, errors.New("agent provider failed")
}

func generatedTestAct(planned app.TeachingWorkflowNode, speaker string, decision agent.ActDecision) agent.ActOutput {
	node := app.TeachingWorkflowNode{
		ID:      planned.ID,
		Kind:    planned.Kind,
		Title:   planned.Title,
		Summary: firstNonEmptyTest(planned.Summary, planned.Title, "测试章节"),
		Speaker: speaker,
		Lines: []app.DialogueLine{
			{Speaker: speaker, Text: "第一句。", SpeechText: "一つ目。", Expression: "soft_smile"},
			{Speaker: speaker, Text: "第二句。", SpeechText: "二つ目。", Expression: "thinking"},
			{Speaker: speaker, Text: "第三句。", SpeechText: "三つ目。", Expression: "curious"},
			{Speaker: speaker, Text: "第四句。", SpeechText: "四つ目。", Expression: "calm"},
		},
	}
	if node.Kind == "opening" || node.Kind == "lesson" {
		node.Choices = testSceneChoices()
	}
	return agent.ActOutput{
		Decision: decision,
		Node:     node,
	}
}

func testSceneChoices() []app.SceneChoice {
	return []app.SceneChoice{
		{ID: "example", Label: "先看例子", Text: "先用例子讲清楚。"},
		{ID: "term", Label: "先拆术语", Text: "先解释术语。"},
	}
}

func firstNonEmptyTest(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type recordingImageEngine struct {
	lastPrompt    string
	lastCharacter app.Character
}

func (e *recordingImageEngine) Generate(_ context.Context, input image.Input) (app.ImageResult, error) {
	e.lastPrompt = input.Request.Prompt
	e.lastCharacter = input.Character
	return app.ImageResult{Format: "mock", Prompt: input.Request.Prompt, Placeholder: true}, nil
}

type recordingVoiceEngine struct {
	lastText    string
	lastPlan    app.VoicePlan
	lastProfile app.VoiceProfile
	calls       int
}

func (e *recordingVoiceEngine) Synthesize(_ context.Context, input voice.Input) (app.AudioResult, error) {
	e.calls++
	e.lastText = input.Text
	e.lastPlan = input.Plan
	e.lastProfile = input.Profile
	return app.AudioResult{URL: "/audio/test.mp3", Format: "mp3", Placeholder: false}, nil
}

type failingVoiceEngine struct {
	err error
}

func (e failingVoiceEngine) Synthesize(context.Context, voice.Input) (app.AudioResult, error) {
	if e.err != nil {
		return app.AudioResult{}, e.err
	}
	return app.AudioResult{}, errors.New("voice provider failed")
}

type cloneVoiceEngine struct {
	provider string
}

func (e *cloneVoiceEngine) Synthesize(context.Context, voice.Input) (app.AudioResult, error) {
	return app.AudioResult{Format: "mp3", Placeholder: true}, nil
}

func (e *cloneVoiceEngine) CloneVoice(_ context.Context, request app.VoiceCloneRequest) (app.VoiceCloneResult, error) {
	e.provider = request.Provider
	return app.VoiceCloneResult{SpeakerID: request.SpeakerID, ResourceID: request.ResourceID, Status: "submitted"}, nil
}

func (e *cloneVoiceEngine) CloneStatus(_ context.Context, request app.VoiceCloneRequest) (app.VoiceCloneResult, error) {
	e.provider = request.Provider
	return app.VoiceCloneResult{SpeakerID: request.SpeakerID, ResourceID: request.ResourceID, Status: "success"}, nil
}

func asyncSceneRequest() app.SceneGenerateRequest {
	return app.SceneGenerateRequest{
		Topic:          "异步生成",
		MaterialSource: textMaterial("异步生成需要先创建生成中的记录，然后后台完成情景和语音准备。"),
		LearningGoal:   "理解非阻塞生成流程。",
		Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "mock"}},
		Runtime: app.RuntimeConfig{
			SceneProvider: string(scene.ProviderMock),
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
			ImageProvider: string(image.ProviderMock),
		},
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}

func waitForGenerationStatus(t *testing.T, store *runtime.FileSessionStore, sessionID string, status string) app.SessionRecord {
	t.Helper()
	return waitForGenerationStatusIn(t, store, sessionID, status)
}

func waitForGenerationStatusIn(t *testing.T, store *runtime.FileSessionStore, sessionID string, statuses ...string) app.SessionRecord {
	t.Helper()
	allowed := map[string]bool{}
	for _, status := range statuses {
		allowed[status] = true
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := store.Get(sessionID)
		if err != nil {
			t.Fatalf("Get() while waiting generation status error = %v", err)
		}
		if allowed[record.Generation.Status] {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("generation status = %q, want one of %v; workflow nodes: %s", record.Generation.Status, statuses, workflowNodeDebugSummary(record.Workflow.Nodes))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func workflowNodeDebugSummary(nodes []app.TeachingWorkflowNode) string {
	parts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		parts = append(parts, fmt.Sprintf("%s/%s status=%s voice=%s decision=%s next=%s choices=%d error=%s", node.ID, node.Kind, node.Status, node.VoiceStatus, node.Decision, node.NextNodeID, len(node.Choices), node.PrepareError))
	}
	return strings.Join(parts, " | ")
}

func waitForStoredWorkflowNodeReady(t *testing.T, store *runtime.FileSessionStore, sessionID string, nodeID string) app.SessionRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := store.Get(sessionID)
		if err != nil {
			t.Fatalf("Get() while waiting workflow node error = %v", err)
		}
		node := workflowNodeByID(record.Workflow.Nodes, nodeID)
		if node.Status == app.WorkflowNodeStatusReady && node.VoiceStatus == app.WorkflowNodeStatusReady {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow node %s status = %q/%q, preparing=%v pending=%q, want ready/ready", nodeID, node.Status, node.VoiceStatus, record.Workflow.Preparing, record.Workflow.PendingNodeID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func nextWorkflowNodeID(t *testing.T, workflow app.TeachingWorkflow, currentID string) string {
	t.Helper()
	for _, node := range workflow.Nodes {
		if node.ID == currentID {
			if node.NextNodeID == "" {
				t.Fatalf("workflow node %s has no next_node_id", currentID)
			}
			return node.NextNodeID
		}
	}
	t.Fatalf("workflow node missing: %s", currentID)
	return ""
}

func waitForWorkflowNextNodeID(t *testing.T, rt *runtime.Runtime, sessionID string, currentID string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := rt.Session(sessionID)
		if err != nil {
			t.Fatalf("Session() while waiting next node error = %v", err)
		}
		current := workflowNodeByID(record.Workflow.Nodes, currentID)
		if current.NextNodeID != "" {
			return current.NextNodeID
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow node %s has no next_node_id", currentID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func workflowNodeByID(nodes []app.TeachingWorkflowNode, id string) app.TeachingWorkflowNode {
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	return app.TeachingWorkflowNode{}
}

func runtimeEventByType(events []app.RuntimeEvent, eventType string) app.RuntimeEvent {
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	return app.RuntimeEvent{}
}

func readyWorkflowNode(id string, kind string, nextID string) app.TeachingWorkflowNode {
	return app.TeachingWorkflowNode{
		ID:          id,
		Kind:        kind,
		Title:       id,
		Speaker:     "亚托莉",
		NextNodeID:  nextID,
		Status:      app.WorkflowNodeStatusReady,
		VoiceStatus: app.WorkflowNodeStatusReady,
		Lines: []app.DialogueLine{{
			Speaker:     "亚托莉",
			Text:        id,
			SpeechText:  id,
			AudioStatus: app.DialogueAudioStatusReady,
			Audio:       app.AudioResult{URL: "/audio/" + id + ".mp3", Format: "mp3"},
		}},
	}
}

func waitForWorkflowSettled(t *testing.T, rt *runtime.Runtime, sessionID string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := rt.Session(sessionID)
		if err != nil {
			t.Fatalf("Session() while waiting settled error = %v", err)
		}
		settled := !record.Workflow.Preparing && strings.TrimSpace(record.Workflow.PendingNodeID) == ""
		for _, node := range record.Workflow.Nodes {
			if node.Kind == "free_discussion" {
				continue
			}
			if node.Status == app.WorkflowNodeStatusPending || node.Status == app.WorkflowNodeStatusSynthesizing {
				settled = false
				break
			}
			if node.VoiceStatus == app.WorkflowNodeStatusPending || node.VoiceStatus == app.WorkflowNodeStatusSynthesizing {
				settled = false
				break
			}
		}
		if settled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("workflow did not settle before timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForWorkflowNodeReady(t *testing.T, rt *runtime.Runtime, sessionID string, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := rt.Session(sessionID)
		if err != nil {
			t.Fatalf("Session() while waiting node ready error = %v", err)
		}
		node := workflowNodeByID(record.Workflow.Nodes, nodeID)
		if node.Status == app.WorkflowNodeStatusReady {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %s status = %q, want ready", nodeID, node.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
