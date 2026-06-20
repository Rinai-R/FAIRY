package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
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
		Topic:        "注意力机制",
		DocumentText: "# 注意力机制\n注意力机制用于让模型关注输入中的重要信息。",
		LearningGoal: "能够解释注意力机制解决什么问题。",
		Characters:   characters,
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
			want: "document_text、document_url 或 document_asset 不能为空",
		},
		{
			name: "missing character",
			request: app.SceneGenerateRequest{
				DocumentText: "课程材料",
			},
			want: "只支持 1 个角色",
		},
		{
			name: "multiple characters",
			request: app.SceneGenerateRequest{
				DocumentText: "课程材料",
				Characters:   []app.Character{{ID: "tutor"}, {ID: "skeptic"}},
			},
			want: "只支持 1 个角色",
		},
		{
			name: "empty character id",
			request: app.SceneGenerateRequest{
				DocumentText: "课程材料",
				Characters:   []app.Character{{DisplayName: "Tutor"}},
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
		Topic:        "Go 调度器",
		DocumentText: "Go 调度器会管理 goroutine、M 和 P。",
		LearningGoal: "理解 GMP",
		Characters:   []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "mock"}},
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
}

func TestAdvanceWorkflowPersistsCurrentNode(t *testing.T) {
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
		Sessions:     store,
		Logger:       slog.Default(),
	})

	resp, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:        "剧情推进",
		DocumentText: "剧情需要把固定讲解段落和最后的自由讨论串起来。",
		Characters:   []app.Character{{ID: "tutor", DisplayName: "Tutor"}},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}
	nextNodeID := waitForWorkflowNextNodeID(t, rt, resp.Session.ID, "opening")
	waitForWorkflowNodeReady(t, rt, resp.Session.ID, nextNodeID)

	advanced, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     resp.Session.ID,
		CurrentNodeID: "opening",
		NextNodeID:    nextNodeID,
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow() error = %v", err)
	}
	if !advanced.Ready || advanced.Waiting {
		t.Fatalf("advance ready flags = ready:%v waiting:%v", advanced.Ready, advanced.Waiting)
	}
	if advanced.Workflow.CurrentNodeID != nextNodeID {
		t.Fatalf("current node = %q, want %s", advanced.Workflow.CurrentNodeID, nextNodeID)
	}
	if advanced.Node.ID != nextNodeID {
		t.Fatalf("response node = %q, want %s", advanced.Node.ID, nextNodeID)
	}

	record, err := rt.Session(resp.Session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if record.Workflow.CurrentNodeID != nextNodeID {
		t.Fatalf("persisted current node = %q, want %s", record.Workflow.CurrentNodeID, nextNodeID)
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

func TestAdvanceWorkflowChoiceWaitsForMissingBranch(t *testing.T) {
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

func TestGenerateSceneAcceptsURLAndUploadedFileSources(t *testing.T) {
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

	for _, tt := range []struct {
		name      string
		variables map[string]string
	}{
		{
			name:      "url source",
			variables: map[string]string{"document_url": "https://example.com/lesson"},
		},
		{
			name:      "uploaded file source",
			variables: map[string]string{"document_asset_path": "data/materials/lesson.png"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
				Topic:      "网络材料",
				Characters: []app.Character{{ID: "tutor"}},
				Variables:  tt.variables,
			})
			if err != nil {
				t.Fatalf("GenerateScene() error = %v", err)
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
		Variables: map[string]string{
			"document_asset_path": asset.Path,
			"document_asset_type": asset.ContentType,
		},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}
	record, err := rt.Session(resp.Session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if !strings.Contains(record.Teaching.DocumentText, "沿负梯度方向更新参数") {
		t.Fatalf("teaching document text was not injected: %q", record.Teaching.DocumentText)
	}
	if record.Teaching.Variables["document_text_source"] != "uploaded_text_asset" {
		t.Fatalf("document_text_source = %q, want uploaded_text_asset", record.Teaching.Variables["document_text_source"])
	}
	waitForWorkflowSettled(t, rt, resp.Session.ID)
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
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentmock.MockEngine{},
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock: scenemock.Engine{},
		},
		Voices: map[voice.Provider]voice.Engine{
			testVoiceProvider: voiceEngine,
		},
		DefaultAgent: agent.ProviderMock,
		DefaultScene: scene.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	sceneResp, err := rt.GenerateScene(context.Background(), app.SceneGenerateRequest{
		Topic:        "语音缓存",
		DocumentText: "语音缓存需要绑定到剧情段落，恢复历史后直接复用。",
		Characters:   []app.Character{{ID: "tutor", DisplayName: "Tutor", VoiceID: "mock"}},
	})
	if err != nil {
		t.Fatalf("GenerateScene() error = %v", err)
	}
	nodeID := waitForWorkflowNextNodeID(t, rt, sceneResp.Session.ID, "opening")
	if _, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     sceneResp.Session.ID,
		CurrentNodeID: "opening",
		NextNodeID:    nodeID,
	}); err != nil {
		t.Fatalf("AdvanceWorkflow() error = %v", err)
	}
	audio, err := rt.SynthesizeVoice(context.Background(), app.VoiceSynthesisRequest{
		Provider:       string(testVoiceProvider),
		Text:           "同じ台詞です。",
		SessionID:      sceneResp.Session.ID,
		WorkflowNodeID: nodeID,
		Plan:           app.VoicePlan{VoiceID: "mock", Style: "calm", Speed: 1, Pitch: 1},
		Character:      app.Character{ID: "tutor", VoiceID: "mock"},
		Profile:        app.VoiceProfile{MediaType: "mp3"},
	})
	if err != nil {
		t.Fatalf("SynthesizeVoice() error = %v", err)
	}
	record, err := rt.Session(sceneResp.Session.ID)
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
		Topic:        "调度器",
		DocumentText: "GMP 调度模型",
		LearningGoal: "理解 GMP",
		Characters:   []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		Runtime:      app.RuntimeConfig{SceneProvider: "mock"},
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
	if completed.Generation.Status != app.SceneGenerationStatusReady {
		t.Fatalf("completed status = %q, want ready", completed.Generation.Status)
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

func TestStartSceneGenerationCreatesPendingAndDeduplicates(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	started := make(chan struct{})
	release := make(chan struct{})
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentmock.MockEngine{}},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		Scenes:       map[scene.Provider]scene.Engine{scene.ProviderMock: blockingSceneEngine{started: started, release: release}},
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
	record := waitForGenerationStatus(t, store, first.Record.Session.ID, app.SceneGenerationStatusReady)
	if record.Scene.ID == "" || len(record.Workflow.Nodes) == 0 {
		t.Fatalf("ready record missing scene/workflow: %+v", record)
	}
}

func TestStartSceneGenerationStoresFailure(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	rt := runtime.NewRuntime(runtime.Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentmock.MockEngine{}},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &recordingVoiceEngine{}},
		Images:       map[image.Provider]image.Engine{image.ProviderMock: imagemock.Engine{}},
		Scenes:       map[scene.Provider]scene.Engine{scene.ProviderMock: failingSceneEngine{err: errors.New("scene provider down")}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		DefaultImage: image.ProviderMock,
		DefaultScene: scene.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	resp, err := rt.StartSceneGeneration(context.Background(), asyncSceneRequest())
	if err != nil {
		t.Fatalf("StartSceneGeneration() error = %v", err)
	}
	record := waitForGenerationStatus(t, store, resp.Record.Session.ID, app.SceneGenerationStatusFailed)
	if !strings.Contains(record.Generation.Error, "scene provider down") {
		t.Fatalf("generation error = %q", record.Generation.Error)
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
	record := waitForGenerationStatus(t, store, "generation:resume", app.SceneGenerationStatusReady)
	if record.Session.ID != "generation:resume" {
		t.Fatalf("resumed session id = %q", record.Session.ID)
	}
}

func TestRuntimeResumesWorkflowFollowupsOnStartup(t *testing.T) {
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	request := asyncSceneRequest()
	request.Topic = "节点恢复"
	request.DocumentText = "第一幕完成后，第二幕必须在重启后继续生成。"
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
	request.DocumentText = "选项分支必须在重启后补齐。"
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
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
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
		},
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

type blockingSceneEngine struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (e blockingSceneEngine) Generate(ctx context.Context, input scene.Input) (app.SceneGenerateResponse, error) {
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
			return app.SceneGenerateResponse{}, ctx.Err()
		}
	}
	return scenemock.Engine{}.Generate(ctx, input)
}

type failingSceneEngine struct {
	err error
}

func (e failingSceneEngine) Generate(context.Context, scene.Input) (app.SceneGenerateResponse, error) {
	if e.err != nil {
		return app.SceneGenerateResponse{}, e.err
	}
	return app.SceneGenerateResponse{}, errors.New("scene provider failed")
}

func asyncSceneRequest() app.SceneGenerateRequest {
	return app.SceneGenerateRequest{
		Topic:        "异步生成",
		DocumentText: "异步生成需要先创建生成中的记录，然后后台完成情景和语音准备。",
		LearningGoal: "理解非阻塞生成流程。",
		Characters:   []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "mock"}},
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := store.Get(sessionID)
		if err != nil {
			t.Fatalf("Get() while waiting generation status error = %v", err)
		}
		if record.Generation.Status == status {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("generation status = %q, want %q", record.Generation.Status, status)
		}
		time.Sleep(20 * time.Millisecond)
	}
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
		settled := true
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
