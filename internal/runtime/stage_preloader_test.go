package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestPrepareWorkflowNodeVoiceUsesSpeechText(t *testing.T) {
	voiceEngine := &stageRecordingVoiceEngine{}
	rt := NewRuntime(Dependencies{
		Voices:       map[voice.Provider]voice.Engine{voice.ProviderMock: voiceEngine},
		DefaultVoice: voice.ProviderMock,
		Logger:       slog.Default(),
	})

	node, err := rt.prepareWorkflowNodeVoice(context.Background(), app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "voice-atri"}},
		Runtime: app.RuntimeConfig{
			VoiceProvider: string(voice.ProviderMock),
			Voice:         app.VoiceProfile{MediaType: "mp3"},
			Language: app.LanguagePlan{
				DisplayLanguage: "cn",
				SpeechLanguage:  "ja",
			},
		},
	}, app.TeachingWorkflowNode{
		ID:      "opening",
		Kind:    "opening",
		Title:   "开场",
		Speaker: "亚托莉",
		Lines: []app.DialogueLine{{
			Speaker:    "亚托莉",
			Text:       "我们先从这篇材料的直觉开始。",
			SpeechText: "まず、この資料の直感から始めましょう。",
			Expression: "soft_smile",
		}},
	})
	if err != nil {
		t.Fatalf("prepareWorkflowNodeVoice() error = %v", err)
	}
	if node.Status != app.WorkflowNodeStatusReady {
		t.Fatalf("node.Status = %q, want ready", node.Status)
	}
	if len(node.Lines) != 1 || node.Lines[0].Audio.URL == "" {
		t.Fatalf("prepared lines missing audio: %+v", node.Lines)
	}
	if voiceEngine.lastText != "まず、この資料の直感から始めましょう。" {
		t.Fatalf("voice text = %q, want speech_text", voiceEngine.lastText)
	}
}

func TestPrepareWorkflowNodeVoiceWaitsAllLines(t *testing.T) {
	voiceEngine := &stageRecordingVoiceEngine{}
	rt := NewRuntime(Dependencies{
		Voices:       map[voice.Provider]voice.Engine{voice.ProviderMock: voiceEngine},
		DefaultVoice: voice.ProviderMock,
		Logger:       slog.Default(),
	})

	node, err := rt.prepareWorkflowNodeVoice(context.Background(), app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "voice-atri"}},
		Runtime:    app.RuntimeConfig{VoiceProvider: string(voice.ProviderMock), Voice: app.VoiceProfile{MediaType: "mp3"}},
	}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
		Lines: []app.DialogueLine{
			{Text: "第一句", SpeechText: "一つ目の台詞です。", Expression: "calm"},
			{Text: "第二句", SpeechText: "二つ目の台詞です。", Expression: "curious"},
		},
	})
	if err != nil {
		t.Fatalf("prepareWorkflowNodeVoice() error = %v", err)
	}
	if node.Status != app.WorkflowNodeStatusReady || node.VoiceStatus != app.WorkflowNodeStatusReady {
		t.Fatalf("node status = %q/%q, want ready/ready", node.Status, node.VoiceStatus)
	}
	for index, line := range node.Lines {
		if line.AudioStatus != app.DialogueAudioStatusReady || line.Audio.URL == "" {
			t.Fatalf("line %d audio = status %q url %q", index, line.AudioStatus, line.Audio.URL)
		}
	}
	if voiceEngine.calls != 2 {
		t.Fatalf("voice calls = %d, want 2", voiceEngine.calls)
	}
}

func TestPreloadRemainingWorkflowNodesStopsWhenPendingNodeIsNotMaterialized(t *testing.T) {
	store := &stageSessionStore{
		record: app.SessionRecord{
			Session: app.Session{ID: "lesson:test", UserID: "default"},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Preparing:     true,
				PendingNodeID: "lesson-1",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						NextNodeID:  "lesson-1",
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{}, "lesson:test", "opening")
	time.Sleep(120 * time.Millisecond)

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.updateCalls != 0 {
		t.Fatalf("update calls = %d, want 0", store.updateCalls)
	}
	if store.lastUpdatedID != "" {
		t.Fatalf("last updated id = %q, want empty", store.lastUpdatedID)
	}
}

func TestPreloadRemainingWorkflowNodesCoalescesDuplicateJobs(t *testing.T) {
	agentEngine := &stageBlockingAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	store := &stageSessionStore{
		record: app.SessionRecord{
			Session: app.Session{ID: "lesson:test", UserID: "default"},
			Characters: []app.Character{{
				ID:          "atri",
				DisplayName: "亚托莉",
				VoiceID:     "voice-atri",
			}},
			Teaching: app.TeachingSnapshot{
				Runtime: app.RuntimeConfig{
					AgentProvider: string(agent.ProviderMock),
					VoiceProvider: string(voice.ProviderMock),
				},
			},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						Decision:    string(agent.ActDecisionContinue),
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{voice.ProviderMock: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: voice.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	request := app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(voice.ProviderMock),
		},
	}
	rt.preloadRemainingWorkflowNodes(request, "lesson:test", "opening")
	rt.preloadRemainingWorkflowNodes(request, "lesson:test", "opening")

	select {
	case <-agentEngine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent was not called")
	}
	time.Sleep(80 * time.Millisecond)
	if got := agentEngine.callCount(); got != 1 {
		t.Fatalf("agent calls = %d, want 1", got)
	}

	close(agentEngine.release)
	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		return findWorkflowNode(record.Workflow.Nodes, "lesson-1").Status == app.WorkflowNodeStatusReady
	}, "workflow should finish the single coalesced preload job")
}

func TestPreloadMarksPendingWithoutSkeletonThenAppendsPreparedNode(t *testing.T) {
	agentEngine := &stageBlockingAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	store := &stageSessionStore{
		record: app.SessionRecord{
			Session: app.Session{ID: "lesson:test", UserID: "default"},
			Characters: []app.Character{{
				ID:          "atri",
				DisplayName: "亚托莉",
				VoiceID:     "voice-atri",
			}},
			Teaching: app.TeachingSnapshot{
				Topic:        "Go 调度器",
				DocumentText: "GMP 模型用于解释 goroutine、线程和处理器上下文如何配合。",
				Runtime: app.RuntimeConfig{
					AgentProvider: string(agent.ProviderMock),
					VoiceProvider: string(voice.ProviderMock),
				},
			},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						Decision:    string(agent.ActDecisionContinue),
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: agentEngine,
		},
		Voices: map[voice.Provider]voice.Engine{
			voice.ProviderMock: &stageRecordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: voice.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(voice.ProviderMock),
		},
	}, "lesson:test", "opening")

	select {
	case <-agentEngine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent was not called")
	}

	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		opening := findWorkflowNode(record.Workflow.Nodes, "opening")
		return record.Workflow.Preparing &&
			record.Workflow.PendingNodeID == "lesson-1" &&
			opening.NextNodeID == "lesson-1" &&
			findWorkflowNode(record.Workflow.Nodes, "lesson-1").ID == ""
	}, "workflow should mark pending next node without appending skeleton")

	close(agentEngine.release)

	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		next := findWorkflowNode(record.Workflow.Nodes, "lesson-1")
		return !record.Workflow.Preparing &&
			record.Workflow.PendingNodeID == "" &&
			next.ID == "lesson-1" &&
			next.Status == app.WorkflowNodeStatusReady &&
			len(next.Lines) >= 3
	}, "workflow should append only the prepared next node")
}

func TestPreloadDoesNotStartFollowingActBeforeAdvance(t *testing.T) {
	agentEngine := &stageBlockingAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	store := &stageSessionStore{
		record: app.SessionRecord{
			Session: app.Session{ID: "lesson:test", UserID: "default"},
			Characters: []app.Character{{
				ID:          "atri",
				DisplayName: "亚托莉",
				VoiceID:     "voice-atri",
			}},
			Teaching: app.TeachingSnapshot{
				Runtime: app.RuntimeConfig{
					AgentProvider: string(agent.ProviderMock),
					VoiceProvider: string(voice.ProviderMock),
				},
			},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						Decision:    string(agent.ActDecisionContinue),
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{voice.ProviderMock: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: voice.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(voice.ProviderMock),
		},
	}, "lesson:test", "opening")

	select {
	case <-agentEngine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent was not called")
	}
	close(agentEngine.release)
	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		return findWorkflowNode(record.Workflow.Nodes, "lesson-1").Status == app.WorkflowNodeStatusReady
	}, "first generated act should become ready")

	time.Sleep(120 * time.Millisecond)
	record, err := store.Get("lesson:test")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if got := agentEngine.callCount(); got != 1 {
		t.Fatalf("agent calls = %d, want 1 before advancing to generated act", got)
	}
	if next := findWorkflowNode(record.Workflow.Nodes, "lesson-2"); next.ID != "" {
		t.Fatalf("unexpected following act before advance: %+v", next)
	}
}

func TestPreloadDoesNotExposeNodeBeforeVoiceReady(t *testing.T) {
	agentEngine := &stageBlockingAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(agentEngine.release)
	voiceEngine := &stageBlockingVoiceEngine{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	store := &stageSessionStore{
		record: app.SessionRecord{
			Session: app.Session{ID: "lesson:test", UserID: "default"},
			Characters: []app.Character{{
				ID:          "atri",
				DisplayName: "亚托莉",
				VoiceID:     "voice-atri",
			}},
			Teaching: app.TeachingSnapshot{
				Runtime: app.RuntimeConfig{
					AgentProvider: string(agent.ProviderMock),
					VoiceProvider: string(voice.ProviderMock),
				},
			},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						Decision:    string(agent.ActDecisionContinue),
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{voice.ProviderMock: voiceEngine},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: voice.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(voice.ProviderMock),
		},
	}, "lesson:test", "opening")

	select {
	case <-voiceEngine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("voice synthesis was not called")
	}
	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		opening := findWorkflowNode(record.Workflow.Nodes, "opening")
		return record.Workflow.Preparing &&
			record.Workflow.PendingNodeID == "lesson-1" &&
			opening.NextNodeID == "lesson-1" &&
			findWorkflowNode(record.Workflow.Nodes, "lesson-1").ID == ""
	}, "workflow should not expose generated text while voice is still blocked")

	close(voiceEngine.release)
	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		next := findWorkflowNode(record.Workflow.Nodes, "lesson-1")
		return !record.Workflow.Preparing &&
			next.ID == "lesson-1" &&
			next.Status == app.WorkflowNodeStatusReady &&
			next.VoiceStatus == app.WorkflowNodeStatusReady &&
			len(next.Lines) == 3
	}, "workflow should append ready node only after all line voices are done")
}

func TestAdvanceWorkflowReturnsWaitingForPendingNextNode(t *testing.T) {
	store := &stageSessionStore{
		record: app.SessionRecord{
			Session: app.Session{ID: "lesson:test", UserID: "default"},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Preparing:     true,
				PendingNodeID: "lesson-1",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						NextNodeID:  "lesson-1",
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})

	resp, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:test",
		CurrentNodeID: "opening",
		NextNodeID:    "lesson-1",
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow() error = %v", err)
	}
	if !resp.Waiting || resp.Ready {
		t.Fatalf("AdvanceWorkflow() waiting/ready = %v/%v, want true/false", resp.Waiting, resp.Ready)
	}
	if resp.Node.ID != "opening" {
		t.Fatalf("waiting node = %q, want opening", resp.Node.ID)
	}
}

func TestAdvanceWorkflowReturnsFailureForErroredNextNode(t *testing.T) {
	store := &stageSessionStore{
		record: app.SessionRecord{
			Session: app.Session{ID: "lesson:test", UserID: "default"},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						NextNodeID:  "lesson-1",
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
					},
					{
						ID:           "lesson-1",
						Kind:         "lesson",
						Title:        "第一幕",
						Status:       app.WorkflowNodeStatusError,
						VoiceStatus:  app.WorkflowNodeStatusError,
						PrepareError: "语音合成失败",
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})

	resp, err := rt.AdvanceWorkflow(context.Background(), app.WorkflowAdvanceRequest{
		SessionID:     "lesson:test",
		CurrentNodeID: "opening",
		NextNodeID:    "lesson-1",
	})
	if err != nil {
		t.Fatalf("AdvanceWorkflow() error = %v", err)
	}
	if resp.Waiting || resp.Ready {
		t.Fatalf("AdvanceWorkflow() waiting/ready = %v/%v, want false/false", resp.Waiting, resp.Ready)
	}
	if !strings.Contains(resp.Message, "语音合成失败") {
		t.Fatalf("AdvanceWorkflow() message = %q, want prepare error", resp.Message)
	}
}

func TestGenerateWorkflowNodeActRejectsDisplaySpeechLanguageMix(t *testing.T) {
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: stageLanguageAgent{},
		},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			Language: app.LanguagePlan{
				DisplayLanguage: "zh-CN",
				SpeechLanguage:  "ja",
				Mode:            "translate_for_voice",
			},
		},
	}, app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil")
	}
	if !strings.Contains(err.Error(), "屏幕显示语言") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want display language error", err)
	}
}

type stageRecordingVoiceEngine struct {
	mu       sync.Mutex
	lastText string
	calls    int
}

func (e *stageRecordingVoiceEngine) Synthesize(_ context.Context, input voice.Input) (app.AudioResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.lastText = input.Text
	return app.AudioResult{URL: "/audio/stage-test.mp3", Format: "mp3", Placeholder: false}, nil
}

type stageBlockingVoiceEngine struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (e *stageBlockingVoiceEngine) Synthesize(ctx context.Context, input voice.Input) (app.AudioResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	e.once.Do(func() { close(e.started) })
	select {
	case <-ctx.Done():
		return app.AudioResult{}, ctx.Err()
	case <-e.release:
	}
	return app.AudioResult{URL: "/audio/" + strings.ReplaceAll(input.Text, " ", "_") + ".mp3", Format: "mp3"}, nil
}

type stageBlockingAgent struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (e *stageBlockingAgent) GenerateAct(ctx context.Context, input agent.ActInput) (agent.ActOutput, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	e.once.Do(func() { close(e.started) })
	select {
	case <-ctx.Done():
		return agent.ActOutput{}, ctx.Err()
	case <-e.release:
	}
	speaker := "亚托莉"
	if len(input.Request.Characters) > 0 {
		speaker = input.Request.Characters[0].DisplayName
	}
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
			ID:      input.PlannedNode.ID,
			Kind:    input.PlannedNode.Kind,
			Title:   input.PlannedNode.Title,
			Summary: "GMP 模型",
			Speaker: speaker,
			Lines: []app.DialogueLine{
				{Speaker: speaker, Text: "G 是 goroutine。", SpeechText: "G は goroutine です。", Expression: "soft_smile"},
				{Speaker: speaker, Text: "M 是系统线程。", SpeechText: "M は OS スレッドです。", Expression: "thinking"},
				{Speaker: speaker, Text: "P 管理可执行上下文。", SpeechText: "P は実行できる文脈を管理します。", Expression: "curious"},
			},
		},
	}, nil
}

func (e *stageBlockingAgent) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *stageBlockingAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

type stageLanguageAgent struct{}

func (stageLanguageAgent) GenerateAct(context.Context, agent.ActInput) (agent.ActOutput, error) {
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
			ID:      "lesson-1",
			Kind:    "lesson",
			Title:   "第一幕",
			Summary: "GMP",
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "こんにちは、今日は GMP を見ます。", SpeechText: "こんにちは、今日は GMP を見ます。", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "M はスレッドです。", SpeechText: "M はスレッドです。", Expression: "thinking"},
				{Speaker: "亚托莉", Text: "P は文脈です。", SpeechText: "P は文脈です。", Expression: "curious"},
			},
		},
	}, nil
}

func (stageLanguageAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

func waitForStageStore(t *testing.T, store *stageSessionStore, ready func(app.SessionRecord) bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := store.Get("lesson:test")
		if err != nil {
			t.Fatalf("store.Get() error = %v", err)
		}
		if ready(record) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s; workflow=%+v", message, record.Workflow)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type stageSessionStore struct {
	mu            sync.Mutex
	record        app.SessionRecord
	updateCalls   int
	lastUpdatedID string
}

func (s *stageSessionStore) BeginScene(app.SceneGenerateRequest, app.SceneGenerateResponse) (app.SessionRecord, error) {
	return app.SessionRecord{}, fmt.Errorf("not implemented")
}

func (s *stageSessionStore) AppendTurn(app.TurnRequest, app.TurnResponse) (app.SessionRecord, error) {
	return app.SessionRecord{}, fmt.Errorf("not implemented")
}

func (s *stageSessionStore) AdvanceWorkflow(app.WorkflowAdvanceRequest) (app.SessionRecord, error) {
	return app.SessionRecord{}, fmt.Errorf("not implemented")
}

func (s *stageSessionStore) AttachWorkflowAudio(string, string, app.AudioResult) (app.SessionRecord, error) {
	return app.SessionRecord{}, fmt.Errorf("not implemented")
}

func (s *stageSessionStore) UpdateWorkflowNode(_ string, node app.TeachingWorkflowNode) (app.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls++
	s.lastUpdatedID = node.ID
	return s.record, nil
}

func (s *stageSessionStore) SaveWorkflow(_ string, workflow app.TeachingWorkflow) (app.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record.Workflow = workflow
	return s.record, nil
}

func (s *stageSessionStore) List() ([]app.SessionRecord, error) {
	return []app.SessionRecord{s.record}, nil
}

func (s *stageSessionStore) Get(string) (app.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, nil
}

func (s *stageSessionStore) Delete(string) error {
	return nil
}
