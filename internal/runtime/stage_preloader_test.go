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

const testVoiceProvider voice.Provider = "test-voice"

func TestRuntimeDefaultPoolsMatchVolcengineConcurrencyLimit(t *testing.T) {
	rt := NewRuntime(Dependencies{})
	t.Cleanup(func() {
		rt.stagePool.Release()
		rt.voicePool.Release()
	})

	if got := rt.stagePool.Cap(); got != 10 {
		t.Fatalf("stage pool cap = %d, want 10", got)
	}
	if got := rt.voicePool.Cap(); got != 10 {
		t.Fatalf("voice pool cap = %d, want 10", got)
	}
}

func TestPrepareWorkflowNodeVoiceUsesSpeechText(t *testing.T) {
	voiceEngine := &stageRecordingVoiceEngine{}
	rt := NewRuntime(Dependencies{
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: voiceEngine},
		DefaultVoice: testVoiceProvider,
		Logger:       slog.Default(),
	})

	node, err := rt.prepareWorkflowNodeVoice(context.Background(), app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "voice-atri"}},
		Runtime: app.RuntimeConfig{
			VoiceProvider: string(testVoiceProvider),
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
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: voiceEngine},
		DefaultVoice: testVoiceProvider,
		Logger:       slog.Default(),
	})

	node, err := rt.prepareWorkflowNodeVoice(context.Background(), app.SceneGenerateRequest{
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉", VoiceID: "voice-atri"}},
		Runtime:    app.RuntimeConfig{VoiceProvider: string(testVoiceProvider), Voice: app.VoiceProfile{MediaType: "mp3"}},
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

func TestWorkflowNodeReadyRequiresDialogueText(t *testing.T) {
	t.Parallel()

	if workflowNodeIsReady(app.TeachingWorkflowNode{
		ID:   "lesson-empty",
		Kind: "lesson",
		Lines: []app.DialogueLine{{
			AudioStatus: app.DialogueAudioStatusReady,
			Audio:       app.AudioResult{URL: "/audio/empty.mp3", Format: "mp3"},
		}},
	}) {
		t.Fatal("workflowNodeIsReady() = true, want false for audio without dialogue text")
	}
}

func TestValidateGeneratedActDialogueUnitsAcceptsChineseOverPromptBudget(t *testing.T) {
	t.Parallel()

	node := app.TeachingWorkflowNode{
		ID:    "lesson-1",
		Kind:  "lesson",
		Title: "第一幕",
		Lines: []app.DialogueLine{
			{Text: "这是第一条短台词。", SpeechText: "一つ目。", Expression: "soft_smile"},
			{Text: "这是第二条短台词。", SpeechText: "二つ目。", Expression: "thinking"},
			{Text: "这是第三条短台词。", SpeechText: "三つ目。", Expression: "curious"},
			{Text: strings.Repeat("很", 53), SpeechText: "四つ目。", Expression: "calm"},
		},
	}

	if err := validateGeneratedActDialogueUnits(node, app.LanguagePlan{DisplayLanguage: "cn"}); err != nil {
		t.Fatalf("validateGeneratedActDialogueUnits() error = %v", err)
	}
}

func TestValidateGeneratedActDialogueUnitsAcceptsEnglishOverPromptBudget(t *testing.T) {
	t.Parallel()

	node := app.TeachingWorkflowNode{
		ID:    "lesson-en",
		Kind:  "lesson",
		Title: "Act",
		Lines: []app.DialogueLine{
			{Text: "First short line.", SpeechText: "First short line.", Expression: "soft_smile"},
			{Text: "Second short line.", SpeechText: "Second short line.", Expression: "thinking"},
			{Text: "Third short line.", SpeechText: "Third short line.", Expression: "curious"},
			{Text: strings.Repeat("a", 121), SpeechText: "Fourth line.", Expression: "calm"},
		},
	}

	if err := validateGeneratedActDialogueUnits(node, app.LanguagePlan{DisplayLanguage: "en"}); err != nil {
		t.Fatalf("validateGeneratedActDialogueUnits() error = %v", err)
	}
}

func TestValidateGeneratedActDialogueUnitsAcceptsFourShortLines(t *testing.T) {
	t.Parallel()

	node := app.TeachingWorkflowNode{
		ID:    "opening",
		Kind:  "opening",
		Title: "开场",
		Lines: []app.DialogueLine{
			{Text: "我们先看核心直觉。", SpeechText: "一つ目。", Expression: "soft_smile"},
			{Text: "再把术语慢慢拆开。", SpeechText: "二つ目。", Expression: "thinking"},
			{Text: "然后用材料里的例子验证。", SpeechText: "三つ目。", Expression: "curious"},
			{Text: "最后再回到你的选择。", SpeechText: "四つ目。", Expression: "calm"},
		},
	}

	if err := validateGeneratedActDialogueUnits(node, app.LanguagePlan{DisplayLanguage: "zh-CN"}); err != nil {
		t.Fatalf("validateGeneratedActDialogueUnits() error = %v", err)
	}
}

func TestWorkflowGenerationStatusReportsLineAudioError(t *testing.T) {
	t.Parallel()

	workflow := app.TeachingWorkflow{
		Nodes: []app.TeachingWorkflowNode{{
			ID:   "lesson-1",
			Kind: "lesson",
			Lines: []app.DialogueLine{{
				Text:        "这句台词有语音错误。",
				AudioStatus: app.DialogueAudioStatusError,
				AudioError:  "voice provider unavailable",
			}},
		}},
	}

	status, message := workflowGenerationStatus(workflow)
	if status != app.SceneGenerationStatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if !strings.Contains(message, "lesson-1") || !strings.Contains(message, "voice provider unavailable") {
		t.Fatalf("message = %q", message)
	}
}

func TestPreloadRemainingWorkflowNodesResumesPendingNodeAfterRestart(t *testing.T) {
	agentEngine := &stageBlockingAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(agentEngine.release)
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
					VoiceProvider: string(testVoiceProvider),
				},
			},
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
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
		},
	}, "lesson:test", "opening")

	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		next := findWorkflowNode(record.Workflow.Nodes, "lesson-1")
		return !record.Workflow.Preparing &&
			record.Workflow.PendingNodeID == "" &&
			next.ID == "lesson-1" &&
			next.Status == app.WorkflowNodeStatusReady &&
			next.VoiceStatus == app.WorkflowNodeStatusReady
	}, "workflow should resume materializing a pending node after restart")
}

func TestPreloadKeepsPendingMarkerUntilResult(t *testing.T) {
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
					VoiceProvider: string(testVoiceProvider),
				},
			},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Preparing:     true,
				PendingNodeID: "lesson-1",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						Decision:    string(agent.ActDecisionContinue),
						NextNodeID:  "lesson-1",
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
		},
	}, "lesson:test", "opening")

	select {
	case <-agentEngine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent was not called")
	}
	for _, workflow := range store.savedWorkflowSnapshots() {
		if !workflow.Preparing && workflow.PendingNodeID == "" && findWorkflowNode(workflow.Nodes, "lesson-1").ID == "" {
			t.Fatalf("workflow had an unrecoverable pending gap: %+v", workflow)
		}
	}

	close(agentEngine.release)
	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		return findWorkflowNode(record.Workflow.Nodes, "lesson-1").Status == app.WorkflowNodeStatusReady
	}, "workflow should finish after preserving pending marker")
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
					VoiceProvider: string(testVoiceProvider),
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
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	request := app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
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
				Topic:          "Go 调度器",
				MaterialSource: app.MaterialSource{Mode: app.MaterialSourceText, Text: "GMP 模型用于解释 goroutine、线程和处理器上下文如何配合。"},
				Runtime: app.RuntimeConfig{
					AgentProvider: string(agent.ProviderMock),
					VoiceProvider: string(testVoiceProvider),
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
			testVoiceProvider: &stageRecordingVoiceEngine{},
		},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
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

func TestPreloadRemainingWorkflowNodesPreparesDirectChoiceBranches(t *testing.T) {
	agentEngine := &stageBlockingAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(agentEngine.release)
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
					VoiceProvider: string(testVoiceProvider),
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
						Choices: []app.SceneChoice{
							{ID: "example", Label: "先看例子", Text: "先用例子继续。"},
							{ID: "term", Label: "先拆术语", Text: "先解释术语。"},
						},
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
		},
	}, "lesson:test", "opening")

	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		opening := findWorkflowNode(record.Workflow.Nodes, "opening")
		if opening.NextNodeID == "" || len(opening.Choices) != 2 {
			return false
		}
		if findWorkflowNode(record.Workflow.Nodes, opening.NextNodeID).Status != app.WorkflowNodeStatusReady {
			return false
		}
		for _, choice := range opening.Choices {
			if strings.TrimSpace(choice.TargetNodeID) == "" {
				return false
			}
			branch := findWorkflowNode(record.Workflow.Nodes, choice.TargetNodeID)
			if branch.ID == "" || branch.Kind != "choice" || !workflowNodeIsReady(branch) {
				return false
			}
		}
		return !record.Workflow.Preparing && record.Workflow.PendingNodeID == ""
	}, "workflow should prepare mainline and every direct choice branch")
	if got := agentEngine.callCount(); got != 3 {
		t.Fatalf("agent calls = %d, want mainline plus two choice branches", got)
	}
}

func TestPreloadRemainingWorkflowNodesPassesCurrentChoiceContextToGenerateAct(t *testing.T) {
	agentEngine := &stageInputRecordingAgent{}
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
					VoiceProvider: string(testVoiceProvider),
				},
			},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Nodes: []app.TeachingWorkflowNode{
					{
						ID:          "opening",
						Kind:        "opening",
						Title:       "开场",
						Summary:     "GMP 总览",
						Decision:    string(agent.ActDecisionContinue),
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
						Choices: []app.SceneChoice{
							{ID: "example", Label: "先看例子", Text: "先用例子继续。"},
							{ID: "term", Label: "先拆术语", Text: "先解释术语。"},
						},
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
		},
	}, "lesson:test", "opening")

	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		opening := findWorkflowNode(record.Workflow.Nodes, "opening")
		if opening.NextNodeID == "" || len(opening.Choices) != 2 {
			return false
		}
		return workflowNodeIsReady(findWorkflowNode(record.Workflow.Nodes, opening.NextNodeID)) &&
			workflowNodeIsReady(findWorkflowNode(record.Workflow.Nodes, opening.Choices[0].TargetNodeID)) &&
			workflowNodeIsReady(findWorkflowNode(record.Workflow.Nodes, opening.Choices[1].TargetNodeID))
	}, "workflow should prepare mainline and choice branches")

	inputs := agentEngine.inputsSnapshot()
	if len(inputs) != 3 {
		t.Fatalf("GenerateAct inputs = %d, want mainline plus two choice branches", len(inputs))
	}
	byPlannedID := map[string]agent.ActInput{}
	for _, input := range inputs {
		byPlannedID[input.PlannedNode.ID] = input
	}
	mainline := byPlannedID["lesson-1"]
	if mainline.PreviousNode.ID != "opening" || mainline.Choice.ID != "" {
		t.Fatalf("mainline context previous=%q choice=%q, want opening and no choice", mainline.PreviousNode.ID, mainline.Choice.ID)
	}
	for _, choice := range findWorkflowNode(store.record.Workflow.Nodes, "opening").Choices {
		input := byPlannedID[choice.TargetNodeID]
		if input.PreviousNode.ID != "opening" {
			t.Fatalf("choice %q previous node = %q, want opening", choice.ID, input.PreviousNode.ID)
		}
		if input.Choice.ID != choice.ID || input.Choice.Label != choice.Label {
			t.Fatalf("choice %q input choice = %+v", choice.ID, input.Choice)
		}
		if len(input.CoveredPoints) == 0 || input.CoveredPoints[0] != "GMP 总览" {
			t.Fatalf("choice %q covered points = %+v, want opening summary", choice.ID, input.CoveredPoints)
		}
	}
}

func TestSessionPreloadsChoiceBranchesWhenDecisionMissing(t *testing.T) {
	agentEngine := &stageBlockingAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(agentEngine.release)
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
					VoiceProvider: string(testVoiceProvider),
				},
			},
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
						Choices: []app.SceneChoice{
							{ID: "example", Label: "先看例子", Text: "先用例子继续。"},
							{ID: "term", Label: "先拆术语", Text: "先解释术语。"},
						},
					},
					{
						ID:          "lesson-1",
						Kind:        "lesson",
						Title:       "第一幕",
						Status:      app.WorkflowNodeStatusReady,
						VoiceStatus: app.WorkflowNodeStatusReady,
						Lines: []app.DialogueLine{{
							Text:        "主线已经准备好了。",
							SpeechText:  "主线已经准备好了。",
							AudioStatus: app.DialogueAudioStatusReady,
							Audio:       app.AudioResult{URL: "/audio/lesson-1.mp3", Format: "mp3"},
						}},
					},
				},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: agentEngine},
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	if _, err := rt.Session("lesson:test"); err != nil {
		t.Fatalf("Session() error = %v", err)
	}

	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		opening := findWorkflowNode(record.Workflow.Nodes, "opening")
		for _, choice := range opening.Choices {
			if strings.TrimSpace(choice.TargetNodeID) == "" {
				return false
			}
			if !workflowNodeIsReady(findWorkflowNode(record.Workflow.Nodes, choice.TargetNodeID)) {
				return false
			}
		}
		return !record.Workflow.Preparing && record.Workflow.PendingNodeID == ""
	}, "Session should preload choices even when decision is absent")
	if got := agentEngine.callCount(); got != 2 {
		t.Fatalf("agent calls = %d, want two choice branches", got)
	}
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
					VoiceProvider: string(testVoiceProvider),
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
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
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

func TestPreloadRecordsRuntimeEventWhenWorkflowNodeFails(t *testing.T) {
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
					VoiceProvider: string(testVoiceProvider),
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
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: &stageRecordingVoiceEngine{}},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
		},
	}, "lesson:test", "opening")

	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		next := findWorkflowNode(record.Workflow.Nodes, "lesson-1")
		return next.ID == "lesson-1" && next.Status == app.WorkflowNodeStatusError
	}, "workflow should persist failed generated node")
	events, err := store.SessionEvents("lesson:test")
	if err != nil {
		t.Fatalf("SessionEvents() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}
	agentEvent := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypeAgentGenerateActFailed)
	if agentEvent.NodeID != "lesson-1" || agentEvent.Stage != app.RuntimeEventStageAgent || agentEvent.Provider != string(agent.ProviderMock) || !strings.Contains(agentEvent.Detail, "agent provider 不可用") {
		t.Fatalf("agent.generate_act.failed event = %+v", agentEvent)
	}
	if agentEvent.DurationMS <= 0 {
		t.Fatalf("agent.generate_act.failed duration_ms = %d, want > 0", agentEvent.DurationMS)
	}
	persistEvent := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypePersistWorkflowSaved)
	if persistEvent.NodeID != "lesson-1" || persistEvent.Stage != app.RuntimeEventStagePersist || persistEvent.DurationMS <= 0 {
		t.Fatalf("persist.workflow.saved event = %+v", persistEvent)
	}
	event := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypeWorkflowNodeFailed)
	if event.NodeID != "lesson-1" || event.Stage != app.RuntimeEventStageWorkflow || !strings.Contains(event.Message, "agent provider 不可用") {
		t.Fatalf("workflow.node.failed event = %+v", event)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("workflow.node.failed duration_ms = %d, want > 0", event.DurationMS)
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
					VoiceProvider: string(testVoiceProvider),
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
		Voices:       map[voice.Provider]voice.Engine{testVoiceProvider: voiceEngine},
		DefaultAgent: agent.ProviderMock,
		DefaultVoice: testVoiceProvider,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	rt.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
		Characters: store.record.Characters,
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			VoiceProvider: string(testVoiceProvider),
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
	var readyRecord app.SessionRecord
	waitForStageStore(t, store, func(record app.SessionRecord) bool {
		next := findWorkflowNode(record.Workflow.Nodes, "lesson-1")
		completeEvent := runtimeEventByTypeForStageTest(record.Events, app.RuntimeEventTypeWorkflowNodeComplete)
		agentEvent := runtimeEventByTypeForStageTest(record.Events, app.RuntimeEventTypeAgentGenerateActDone)
		persistEvent := runtimeEventByTypeForStageTest(record.Events, app.RuntimeEventTypePersistWorkflowSaved)
		ready := !record.Workflow.Preparing &&
			next.ID == "lesson-1" &&
			next.Status == app.WorkflowNodeStatusReady &&
			next.VoiceStatus == app.WorkflowNodeStatusReady &&
			len(next.Lines) == 4 &&
			agentEvent.NodeID == "lesson-1" &&
			agentEvent.Stage == app.RuntimeEventStageAgent &&
			agentEvent.Provider == string(agent.ProviderMock) &&
			agentEvent.DurationMS > 0 &&
			persistEvent.NodeID == "lesson-1" &&
			persistEvent.Stage == app.RuntimeEventStagePersist &&
			persistEvent.DurationMS > 0 &&
			completeEvent.NodeID == "lesson-1" &&
			completeEvent.DurationMS > 0
		if ready {
			readyRecord = record
		}
		return ready
	}, "workflow should append ready node and duration event only after all line voices are done")
	event := runtimeEventByTypeForStageTest(readyRecord.Events, app.RuntimeEventTypeWorkflowNodeComplete)
	if event.Stage != app.RuntimeEventStageWorkflow {
		t.Fatalf("workflow.node.completed event = %+v", event)
	}
	agentEvent := runtimeEventByTypeForStageTest(readyRecord.Events, app.RuntimeEventTypeAgentGenerateActDone)
	if agentEvent.Stage != app.RuntimeEventStageAgent || agentEvent.Provider != string(agent.ProviderMock) {
		t.Fatalf("agent.generate_act.completed event = %+v", agentEvent)
	}
	persistEvent := runtimeEventByTypeForStageTest(readyRecord.Events, app.RuntimeEventTypePersistWorkflowSaved)
	if persistEvent.Stage != app.RuntimeEventStagePersist || persistEvent.NodeID != "lesson-1" {
		t.Fatalf("persist.workflow.saved event = %+v", persistEvent)
	}
}

func TestGenerateWorkflowNodeActPersistsAgentSubstepTraceEvents(t *testing.T) {
	store := &stageSessionStore{record: app.SessionRecord{Session: app.Session{ID: "lesson:test", UserID: "default"}}}
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: stageTracingAgent{},
		},
		DefaultAgent: agent.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	prepared, _, err := rt.generateWorkflowNodeAct(
		context.Background(),
		stageStructureRequest(),
		app.Session{ID: "lesson:test", UserID: "default"},
		app.TeachingWorkflow{},
		app.TeachingWorkflowNode{ID: "lesson-1", Kind: "lesson", Title: "第一幕"},
		app.SceneChoice{},
	)
	if err != nil {
		t.Fatalf("generateWorkflowNodeAct() error = %v", err)
	}
	if prepared.ID != "lesson-1" {
		t.Fatalf("prepared node id = %q", prepared.ID)
	}
	events, err := store.SessionEvents("lesson:test")
	if err != nil {
		t.Fatalf("SessionEvents() error = %v", err)
	}
	actPlan := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypeAgentActPlanCacheHit)
	if actPlan.Stage != app.RuntimeEventStageAgent || actPlan.NodeID != "lesson-1" || actPlan.Provider != string(agent.ProviderMock) || actPlan.Message != "ActPlan 缓存命中。" || actPlan.DurationMS != 7 {
		t.Fatalf("agent.actplan.cache_hit event = %+v", actPlan)
	}
	rewriteRetry := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypeAgentRewriteRetry)
	if rewriteRetry.Level != app.RuntimeEventLevelWarn || rewriteRetry.RetryCount != 1 || !strings.Contains(rewriteRetry.Detail, "covered_points") {
		t.Fatalf("agent.rewrite_act.retry event = %+v", rewriteRetry)
	}
	overall := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypeAgentGenerateActDone)
	if overall.NodeID != "lesson-1" || overall.Stage != app.RuntimeEventStageAgent {
		t.Fatalf("agent.generate_act.completed event = %+v", overall)
	}
}

func TestGenerateWorkflowNodeActDoesNotInventAgentSubstepTraceEvents(t *testing.T) {
	store := &stageSessionStore{record: app.SessionRecord{Session: app.Session{ID: "lesson:test", UserID: "default"}}}
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: stageStructureAgent{},
		},
		DefaultAgent: agent.ProviderMock,
		Sessions:     store,
		Logger:       slog.Default(),
	})

	if _, _, err := rt.generateWorkflowNodeAct(
		context.Background(),
		stageStructureRequest(),
		app.Session{ID: "lesson:test", UserID: "default"},
		app.TeachingWorkflow{},
		app.TeachingWorkflowNode{ID: "lesson-1", Kind: "lesson", Title: "第一幕"},
		app.SceneChoice{},
	); err != nil {
		t.Fatalf("generateWorkflowNodeAct() error = %v", err)
	}
	events, err := store.SessionEvents("lesson:test")
	if err != nil {
		t.Fatalf("SessionEvents() error = %v", err)
	}
	if event := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypeAgentActPlanDone); event.Type != "" {
		t.Fatalf("unexpected agent.actplan.completed event = %+v", event)
	}
	if event := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypeAgentRewriteDone); event.Type != "" {
		t.Fatalf("unexpected agent.rewrite_act.completed event = %+v", event)
	}
	if event := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypeAgentGenerateActDone); event.Type == "" {
		t.Fatalf("missing overall agent.generate_act.completed event in %+v", events)
	}
}

func TestApplyWorkflowStageResultRecordsPersistFailure(t *testing.T) {
	store := &stageSessionStore{
		saveErr: fmt.Errorf("disk full"),
		record: app.SessionRecord{
			Session: app.Session{ID: "lesson:test", UserID: "default"},
			Workflow: app.TeachingWorkflow{
				CurrentNodeID: "opening",
				Nodes: []app.TeachingWorkflowNode{{
					ID:          "opening",
					Kind:        "opening",
					Status:      app.WorkflowNodeStatusReady,
					VoiceStatus: app.WorkflowNodeStatusReady,
				}},
			},
		},
	}
	rt := NewRuntime(Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})

	rt.applyWorkflowStageResult(workflowStageResult{
		sessionID:     "lesson:test",
		plannedNodeID: "lesson-1",
		prepared: app.TeachingWorkflowNode{
			ID:          "lesson-1",
			Kind:        "lesson",
			Status:      app.WorkflowNodeStatusReady,
			VoiceStatus: app.WorkflowNodeStatusReady,
		},
	})

	events, err := store.SessionEvents("lesson:test")
	if err != nil {
		t.Fatalf("SessionEvents() error = %v", err)
	}
	event := runtimeEventByTypeForStageTest(events, app.RuntimeEventTypePersistWorkflowFailed)
	if event.Stage != app.RuntimeEventStagePersist || event.NodeID != "lesson-1" || !strings.Contains(event.Detail, "disk full") {
		t.Fatalf("persist.workflow.failed event = %+v", event)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("persist.workflow.failed duration_ms = %d, want > 0", event.DurationMS)
	}
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
	events, err := store.SessionEvents("lesson:test")
	if err != nil {
		t.Fatalf("SessionEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(events), events)
	}
	event := events[0]
	if event.Type != app.RuntimeEventTypeWorkflowNodeFailed || event.NodeID != "lesson-1" || event.Stage != app.RuntimeEventStageWorkflow || !strings.Contains(event.Message, "语音合成失败") {
		t.Fatalf("workflow.node.failed event = %+v", event)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("workflow.node.failed duration_ms = %d, want > 0", event.DurationMS)
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
	}, app.SceneChoice{})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil")
	}
	if !strings.Contains(err.Error(), "屏幕显示语言") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want display language error", err)
	}
}

func TestGenerateWorkflowNodeActRetriesRuntimeLanguageContract(t *testing.T) {
	ag := &stageRepairingLanguageAgent{}
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: ag,
		},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	node, _, err := rt.generateWorkflowNodeAct(context.Background(), app.SceneGenerateRequest{
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
	}, app.SceneChoice{})
	if err != nil {
		t.Fatalf("generateWorkflowNodeAct() error = %v", err)
	}
	if ag.calls != 2 {
		t.Fatalf("GenerateAct calls = %d, want 2", ag.calls)
	}
	if !strings.Contains(ag.correction, "屏幕显示语言") {
		t.Fatalf("correction = %q, want display language feedback", ag.correction)
	}
	if got := node.Lines[0].Text; strings.Contains(got, "こんにちは") {
		t.Fatalf("node.Lines[0].Text = %q, want repaired Chinese display text", got)
	}
}

func TestGenerateWorkflowNodeActRetriesInvalidExpressionContract(t *testing.T) {
	ag := &stageRepairingExpressionAgent{}
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: ag,
		},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	node, _, err := rt.generateWorkflowNodeAct(context.Background(), app.SceneGenerateRequest{
		Characters: []app.Character{{
			ID:          "atri",
			DisplayName: "亚托莉",
			Assets: app.CharacterAssets{Moods: map[string]app.CharacterMood{
				"focused": {Label: "专注", PortraitURL: "/focused.png"},
			}},
		}},
		MaterialContext: app.MaterialContext{Brief: "GMP 调度材料"},
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
	}, app.SceneChoice{})
	if err != nil {
		t.Fatalf("generateWorkflowNodeAct() error = %v", err)
	}
	if ag.calls != 2 {
		t.Fatalf("GenerateAct calls = %d, want 2", ag.calls)
	}
	if !strings.Contains(ag.correction, "不在角色差分 contract") {
		t.Fatalf("correction = %q, want expression contract feedback", ag.correction)
	}
	for index, line := range node.Lines {
		if line.Expression != "focused" {
			t.Fatalf("line[%d].Expression = %q, want focused", index, line.Expression)
		}
	}
}

func TestGenerateWorkflowNodeActRetriesAgentContractError(t *testing.T) {
	ag := &stageRepairingContractErrorAgent{}
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: ag,
		},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err != nil {
		t.Fatalf("generateWorkflowNodeAct() error = %v", err)
	}
	if ag.calls != 2 {
		t.Fatalf("GenerateAct calls = %d, want 2", ag.calls)
	}
	if !strings.Contains(ag.correction, "decision 不支持") {
		t.Fatalf("correction = %q, want contract error feedback", ag.correction)
	}
}

func TestGenerateWorkflowNodeActAllowsGeneratedDialogueWithoutContentInspection(t *testing.T) {
	ag := stageStructureAgent{}
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: ag,
		},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	node, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err != nil {
		t.Fatalf("generateWorkflowNodeAct() error = %v", err)
	}
	if node.ID != "lesson-1" || len(node.Lines) != 4 {
		t.Fatalf("node = %+v", node)
	}
}

func TestGenerateWorkflowNodeActDoesNotRetryPlainAgentError(t *testing.T) {
	ag := &stagePlainErrorAgent{}
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock: ag,
		},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil, want provider error")
	}
	if ag.calls != 1 {
		t.Fatalf("GenerateAct calls = %d, want 1", ag.calls)
	}
	if !strings.Contains(err.Error(), "agent provider unavailable") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want provider error", err)
	}
}

func TestGenerateWorkflowNodeActRejectsMissingTeachingSummary(t *testing.T) {
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: stageStructureAgent{missingSummary: true}},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil")
	}
	if !strings.Contains(err.Error(), "summary 不能为空") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want summary gate", err)
	}
}

func TestGenerateWorkflowNodeActRejectsMissingTeachingChoices(t *testing.T) {
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: stageStructureAgent{missingChoices: true}},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil")
	}
	if !strings.Contains(err.Error(), "choices 必须提供") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want choices gate", err)
	}
}

func TestGenerateWorkflowNodeActRejectsMissingChoiceLabel(t *testing.T) {
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: stageStructureAgent{emptyChoiceLabel: true}},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil")
	}
	if !strings.Contains(err.Error(), "choices[0].label 不能为空") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want label gate", err)
	}
}

func TestGenerateWorkflowNodeActRejectsLongChoiceLabel(t *testing.T) {
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{agent.ProviderMock: stageStructureAgent{
			choiceLabel: "亚托莉会继续用课堂比喻解释GMP调度器的完整运行过程",
		}},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil")
	}
	if !strings.Contains(err.Error(), "label 必须是短按钮文案") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want short label gate", err)
	}
}

func TestGenerateWorkflowNodeActRejectsChoiceLabelMatchingText(t *testing.T) {
	rt := NewRuntime(Dependencies{
		Agents: map[agent.Provider]agent.Engine{agent.ProviderMock: stageStructureAgent{
			choiceLabel: "先用例子讲清楚。",
			choiceText:  "先用例子讲清楚。",
		}},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil")
	}
	if !strings.Contains(err.Error(), "label 不能与 text 相同") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want label/text separation gate", err)
	}
}

func TestGenerateWorkflowNodeActRejectsEarlyFreeDiscussion(t *testing.T) {
	rt := NewRuntime(Dependencies{
		Agents:       map[agent.Provider]agent.Engine{agent.ProviderMock: stageStructureAgent{decision: agent.ActDecisionFreeDiscussion}},
		DefaultAgent: agent.ProviderMock,
		Logger:       slog.Default(),
	})

	_, _, err := rt.generateWorkflowNodeAct(context.Background(), stageStructureRequest(), app.Session{ID: "lesson:test"}, app.TeachingWorkflow{
		Nodes: []app.TeachingWorkflowNode{{
			ID:      "opening",
			Kind:    "opening",
			Title:   "开场",
			Summary: "开场目标",
			Status:  app.WorkflowNodeStatusReady,
		}},
	}, app.TeachingWorkflowNode{
		ID:      "lesson-1",
		Kind:    "lesson",
		Title:   "第一幕",
		Speaker: "亚托莉",
	}, app.SceneChoice{})
	if err == nil {
		t.Fatal("generateWorkflowNodeAct() error = nil")
	}
	if !strings.Contains(err.Error(), "不能在 summary 前进入自由讨论") {
		t.Fatalf("generateWorkflowNodeAct() error = %v, want early free discussion gate", err)
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
	node := app.TeachingWorkflowNode{
		ID:      input.PlannedNode.ID,
		Kind:    input.PlannedNode.Kind,
		Title:   input.PlannedNode.Title,
		Summary: "GMP 模型",
		Speaker: speaker,
		Lines: []app.DialogueLine{
			{Speaker: speaker, Text: "G 是 goroutine。", SpeechText: "G は goroutine です。", Expression: "soft_smile"},
			{Speaker: speaker, Text: "M 是系统线程。", SpeechText: "M は OS スレッドです。", Expression: "thinking"},
			{Speaker: speaker, Text: "P 管理可执行上下文。", SpeechText: "P は実行できる文脈を管理します。", Expression: "curious"},
			{Speaker: speaker, Text: "三者合起来决定任务如何运行。", SpeechText: "三つが一緒に動かし方を決めます。", Expression: "calm"},
		},
	}
	if node.Kind == "opening" || node.Kind == "lesson" {
		node.Choices = stageTestChoices()
	}
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node:     node,
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

type stageInputRecordingAgent struct {
	mu     sync.Mutex
	inputs []agent.ActInput
}

func (e *stageInputRecordingAgent) GenerateAct(_ context.Context, input agent.ActInput) (agent.ActOutput, error) {
	e.mu.Lock()
	e.inputs = append(e.inputs, input)
	e.mu.Unlock()
	speaker := "亚托莉"
	if len(input.Request.Characters) > 0 {
		speaker = input.Request.Characters[0].DisplayName
	}
	node := app.TeachingWorkflowNode{
		ID:      input.PlannedNode.ID,
		Kind:    input.PlannedNode.Kind,
		Title:   input.PlannedNode.Title,
		Summary: firstNonEmpty(input.PlannedNode.Summary, input.Choice.Label, "GMP 模型"),
		Speaker: speaker,
		Lines: []app.DialogueLine{
			{Speaker: speaker, Text: "G 是 goroutine。", SpeechText: "G は goroutine です。", Expression: "soft_smile"},
			{Speaker: speaker, Text: "M 是系统线程。", SpeechText: "M は OS スレッドです。", Expression: "thinking"},
			{Speaker: speaker, Text: "P 管理可执行上下文。", SpeechText: "P は実行できる文脈を管理します。", Expression: "curious"},
			{Speaker: speaker, Text: "三者合起来决定任务如何运行。", SpeechText: "三つが一緒に動かし方を決めます。", Expression: "calm"},
		},
	}
	if node.Kind == "opening" || node.Kind == "lesson" {
		node.Choices = stageTestChoices()
	}
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node:     node,
	}, nil
}

func (e *stageInputRecordingAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

func (e *stageInputRecordingAgent) inputsSnapshot() []agent.ActInput {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]agent.ActInput, len(e.inputs))
	copy(out, e.inputs)
	return out
}

func stageTestChoices() []app.SceneChoice {
	return []app.SceneChoice{
		{ID: "example", Label: "先看例子", Text: "先用例子讲清楚。"},
		{ID: "term", Label: "先拆术语", Text: "先解释术语。"},
	}
}

type stageStructureAgent struct {
	missingSummary   bool
	missingChoices   bool
	emptyChoiceLabel bool
	choiceLabel      string
	choiceText       string
	decision         agent.ActDecision
}

func (e stageStructureAgent) GenerateAct(context.Context, agent.ActInput) (agent.ActOutput, error) {
	summary := "GMP 分工"
	if e.missingSummary {
		summary = ""
	}
	choices := stageTestChoices()
	if e.missingChoices {
		choices = nil
	}
	if len(choices) > 0 {
		if e.emptyChoiceLabel {
			choices[0].Label = ""
		}
		if e.choiceLabel != "" {
			choices[0].Label = e.choiceLabel
		}
		if e.choiceText != "" {
			choices[0].Text = e.choiceText
		}
	}
	decision := e.decision
	if decision == "" {
		decision = agent.ActDecisionContinue
	}
	return agent.ActOutput{
		Decision: decision,
		Node: app.TeachingWorkflowNode{
			ID:      "lesson-1",
			Kind:    "lesson",
			Title:   "第一幕",
			Summary: summary,
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "今天先看 GMP 的分工。", SpeechText: "今日はまず GMP の役割を見ます。", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "M 负责真正执行代码。", SpeechText: "M は実際にコードを実行します。", Expression: "thinking"},
				{Speaker: "亚托莉", Text: "P 持有可运行任务队列。", SpeechText: "P は実行可能なタスク列を持っています。", Expression: "curious"},
				{Speaker: "亚托莉", Text: "G 就是等待运行的任务。", SpeechText: "G は実行を待っているタスクです。", Expression: "calm"},
			},
			Choices: choices,
		},
	}, nil
}

func (e stageStructureAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

type stageTracingAgent struct{}

func (stageTracingAgent) GenerateAct(ctx context.Context, input agent.ActInput) (agent.ActOutput, error) {
	if input.Trace != nil {
		input.Trace(agent.ActTraceEvent{
			Type:       app.RuntimeEventTypeAgentActPlanCacheHit,
			Level:      app.RuntimeEventLevelInfo,
			Step:       agent.ActTraceStepActPlan,
			DurationMS: 7,
		})
		input.Trace(agent.ActTraceEvent{
			Type:       app.RuntimeEventTypeAgentDraftDone,
			Level:      app.RuntimeEventLevelInfo,
			Step:       agent.ActTraceStepGenerateActDraft,
			Message:    "草稿完成。",
			DurationMS: 11,
		})
		input.Trace(agent.ActTraceEvent{
			Type:       app.RuntimeEventTypeAgentRewriteRetry,
			Level:      app.RuntimeEventLevelWarn,
			Step:       agent.ActTraceStepRewriteAct,
			Detail:     "rewrite covered_points 必须保留草稿 covered_points",
			RetryCount: 1,
			DurationMS: 13,
		})
	}
	return stageStructureAgent{}.GenerateAct(ctx, input)
}

func (stageTracingAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

func stageStructureRequest() app.SceneGenerateRequest {
	return app.SceneGenerateRequest{
		Characters:      []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		MaterialContext: app.MaterialContext{Brief: "GMP 调度材料"},
		Runtime: app.RuntimeConfig{
			AgentProvider: string(agent.ProviderMock),
			Language: app.LanguagePlan{
				DisplayLanguage: "zh-CN",
				SpeechLanguage:  "ja",
				Mode:            "translate_for_voice",
			},
		},
	}
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
				{Speaker: "亚托莉", Text: "G は待っている仕事です。", SpeechText: "G は待っている仕事です。", Expression: "calm"},
			},
		},
	}, nil
}

func (stageLanguageAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

type stageRepairingLanguageAgent struct {
	calls      int
	correction string
}

func (e *stageRepairingLanguageAgent) GenerateAct(_ context.Context, input agent.ActInput) (agent.ActOutput, error) {
	e.calls++
	if e.calls == 1 {
		return stageLanguageAgent{}.GenerateAct(context.Background(), input)
	}
	e.correction = input.Correction
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
			ID:      "lesson-1",
			Kind:    "lesson",
			Title:   "第一幕",
			Summary: "GMP",
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "今天先看 GMP 的分工。", SpeechText: "今日はまず GMP の役割を見ます。", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "M 负责真正执行代码。", SpeechText: "M は実際にコードを実行します。", Expression: "thinking"},
				{Speaker: "亚托莉", Text: "P 持有可运行任务队列。", SpeechText: "P は実行可能なタスク列を持っています。", Expression: "curious"},
				{Speaker: "亚托莉", Text: "G 就是等待运行的任务。", SpeechText: "G は実行を待っているタスクです。", Expression: "calm"},
			},
			Choices: stageTestChoices(),
		},
	}, nil
}

func (e *stageRepairingLanguageAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

type stageRepairingExpressionAgent struct {
	calls      int
	correction string
}

func (e *stageRepairingExpressionAgent) GenerateAct(_ context.Context, input agent.ActInput) (agent.ActOutput, error) {
	e.calls++
	expression := "angry"
	if e.calls > 1 {
		e.correction = input.Correction
		expression = "focused"
	}
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
			ID:      "lesson-1",
			Kind:    "lesson",
			Title:   "第一幕",
			Summary: "GMP",
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "今天先看 GMP 的分工。", SpeechText: "今日はまず GMP の役割を見ます。", Expression: expression},
				{Speaker: "亚托莉", Text: "M 负责真正执行代码。", SpeechText: "M は実際にコードを実行します。", Expression: expression},
				{Speaker: "亚托莉", Text: "P 持有可运行任务队列。", SpeechText: "P は実行可能なタスク列を持っています。", Expression: expression},
				{Speaker: "亚托莉", Text: "G 就是等待运行的任务。", SpeechText: "G は実行を待っているタスクです。", Expression: expression},
			},
			Choices: []app.SceneChoice{{ID: "next", Label: "继续", Text: "继续讲。"}},
		},
	}, nil
}

func (e *stageRepairingExpressionAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

type stageRepairingContractErrorAgent struct {
	calls      int
	correction string
}

func (e *stageRepairingContractErrorAgent) GenerateAct(_ context.Context, input agent.ActInput) (agent.ActOutput, error) {
	e.calls++
	if e.calls == 1 {
		return agent.ActOutput{}, agent.NewContractError(fmt.Errorf("FAIRY GenerateAct 输出连续不符合合约: decision 不支持: "))
	}
	e.correction = input.Correction
	return validStageActOutput(), nil
}

func (e *stageRepairingContractErrorAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

type stagePlainErrorAgent struct {
	calls int
}

func (e *stagePlainErrorAgent) GenerateAct(context.Context, agent.ActInput) (agent.ActOutput, error) {
	e.calls++
	return agent.ActOutput{}, fmt.Errorf("agent provider unavailable")
}

func (e *stagePlainErrorAgent) Discuss(context.Context, agent.DiscussInput) (agent.Output, error) {
	return agent.Output{}, fmt.Errorf("not implemented")
}

func validStageActOutput() agent.ActOutput {
	return agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
			ID:      "lesson-1",
			Kind:    "lesson",
			Title:   "第一幕",
			Summary: "GMP",
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "今天先看 GMP 的分工。", SpeechText: "今日はまず GMP の役割を見ます。", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "M 负责真正执行代码。", SpeechText: "M は実際にコードを実行します。", Expression: "thinking"},
				{Speaker: "亚托莉", Text: "P 持有可运行任务队列。", SpeechText: "P は実行可能なタスク列を持っています。", Expression: "curious"},
				{Speaker: "亚托莉", Text: "G 就是等待运行的任务。", SpeechText: "G は実行を待っているタスクです。", Expression: "calm"},
			},
			Choices: stageTestChoices(),
		},
	}
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

func runtimeEventByTypeForStageTest(events []app.RuntimeEvent, eventType string) app.RuntimeEvent {
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	return app.RuntimeEvent{}
}

type stageSessionStore struct {
	mu             sync.Mutex
	record         app.SessionRecord
	updateCalls    int
	lastUpdatedID  string
	savedWorkflows []app.TeachingWorkflow
	saveErr        error
}

func (s *stageSessionStore) BeginScene(app.SceneGenerateRequest, app.SceneGenerateResponse) (app.SessionRecord, error) {
	return app.SessionRecord{}, fmt.Errorf("not implemented")
}

func (s *stageSessionStore) CreateGeneration(app.SessionRecord) (app.SessionRecord, error) {
	return app.SessionRecord{}, fmt.Errorf("not implemented")
}

func (s *stageSessionStore) CompleteGeneration(string, app.SceneGenerateResponse) (app.SessionRecord, error) {
	return app.SessionRecord{}, fmt.Errorf("not implemented")
}

func (s *stageSessionStore) FailGeneration(string, string) (app.SessionRecord, error) {
	return app.SessionRecord{}, fmt.Errorf("not implemented")
}

func (s *stageSessionStore) GenerationByFingerprint(string) (app.SessionRecord, bool, error) {
	return app.SessionRecord{}, false, nil
}

func (s *stageSessionStore) ListGeneration(string) ([]app.SessionRecord, error) {
	return nil, nil
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
	if s.saveErr != nil {
		return app.SessionRecord{}, s.saveErr
	}
	s.record.Workflow = workflow
	s.savedWorkflows = append(s.savedWorkflows, workflow)
	return s.record, nil
}

func (s *stageSessionStore) AppendEvent(_ string, event app.RuntimeEvent) (app.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record.Events = append(s.record.Events, event)
	return s.record, nil
}

func (s *stageSessionStore) SessionEvents(string) ([]app.RuntimeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]app.RuntimeEvent, len(s.record.Events))
	copy(events, s.record.Events)
	return events, nil
}

func (s *stageSessionStore) savedWorkflowSnapshots() []app.TeachingWorkflow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]app.TeachingWorkflow, len(s.savedWorkflows))
	copy(out, s.savedWorkflows)
	return out
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
