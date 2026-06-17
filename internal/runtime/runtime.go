package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/image"
	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/pkg/async"
)

const (
	defaultStagePoolSize = 8
	defaultVoicePoolSize = 12
)

type Runtime struct {
	agents       map[agent.Provider]agent.Engine
	voices       map[voice.Provider]voice.Engine
	images       map[image.Provider]image.Engine
	scenes       map[scene.Provider]scene.Engine
	defaultAgent agent.Provider
	defaultVoice voice.Provider
	defaultImage image.Provider
	defaultScene scene.Provider
	materialDir  string
	sessions     SessionStore
	plugins      interface{ Load() app.PluginCatalog }
	logger       *slog.Logger
	voiceCache   map[string]app.AudioResult
	cacheMu      sync.Mutex
	stagePool    *async.Pool
	voicePool    *async.Pool
	preloadMu    sync.Mutex
	preloadJobs  map[string]struct{}
}

type Dependencies struct {
	Agents       map[agent.Provider]agent.Engine
	Voices       map[voice.Provider]voice.Engine
	Images       map[image.Provider]image.Engine
	Scenes       map[scene.Provider]scene.Engine
	DefaultAgent agent.Provider
	DefaultVoice voice.Provider
	DefaultImage image.Provider
	DefaultScene scene.Provider
	MaterialDir  string
	Sessions     SessionStore
	Plugins      interface{ Load() app.PluginCatalog }
	Logger       *slog.Logger
}

func NewRuntime(deps Dependencies) *Runtime {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{
		agents:       deps.Agents,
		voices:       deps.Voices,
		images:       deps.Images,
		scenes:       deps.Scenes,
		defaultAgent: deps.DefaultAgent,
		defaultVoice: deps.DefaultVoice,
		defaultImage: deps.DefaultImage,
		defaultScene: deps.DefaultScene,
		materialDir:  deps.MaterialDir,
		sessions:     deps.Sessions,
		plugins:      deps.Plugins,
		logger:       logger,
		voiceCache:   map[string]app.AudioResult{},
		stagePool:    mustNewPool(defaultStagePoolSize),
		voicePool:    mustNewPool(defaultVoicePoolSize),
		preloadJobs:  map[string]struct{}{},
	}
}

func mustNewPool(size int) *async.Pool {
	pool, err := async.NewPool(size)
	if err != nil {
		panic(err)
	}
	return pool
}

func (r *Runtime) Plugins() app.PluginCatalog {
	if r.plugins == nil {
		return app.PluginCatalog{Version: "0.1.0"}
	}
	return r.plugins.Load()
}

func (r *Runtime) GenerateScene(ctx context.Context, request app.SceneGenerateRequest) (app.SceneGenerateResponse, error) {
	var err error
	request, err = r.hydrateSceneDocumentText(request)
	if err != nil {
		return app.SceneGenerateResponse{}, err
	}
	if err := validateSceneGenerateRequest(request); err != nil {
		return app.SceneGenerateResponse{}, err
	}
	engine, err := r.scene(request.Runtime.SceneProvider)
	if err != nil {
		return app.SceneGenerateResponse{}, err
	}
	resp, err := engine.Generate(ctx, scene.Input{Request: request})
	if err != nil {
		return app.SceneGenerateResponse{}, err
	}
	resp.Workflow = initializeDynamicWorkflow(resp.Workflow, resp.Scene.ID, request.Topic, request.LearningGoal)
	openingPlan := app.TeachingWorkflowNode{
		ID:            "opening",
		Kind:          "opening",
		Title:         "开场对白",
		Speaker:       firstSceneSpeaker(request),
		BackgroundKey: "opening",
		BackgroundURL: firstSceneBackground(request),
		Status:        app.WorkflowNodeStatusPending,
		VoiceStatus:   app.WorkflowNodeStatusPending,
	}
	prepared, decision, err := r.prepareWorkflowNodeActAndVoice(ctx, request, resp.Session, resp.Workflow, openingPlan)
	if err != nil {
		return app.SceneGenerateResponse{}, err
	}
	prepared.Decision = string(decision)
	resp.Workflow.CurrentNodeID = prepared.ID
	resp.Workflow.Nodes = []app.TeachingWorkflowNode{prepared}
	resp.Workflow.Preparing = false
	resp.Workflow.PendingNodeID = ""
	ensureWorkflowHistory(&resp.Workflow, time.Now().UTC())
	if r.sessions != nil {
		record, err := r.sessions.BeginScene(request, resp)
		if err != nil {
			r.logger.Warn("写入教学场景失败", "error", err)
		} else {
			resp.Workflow = record.Workflow
			resp.Session = record.Session
			if shouldQueueNextAct(prepared) {
				r.preloadRemainingWorkflowNodes(request, record.Session.ID, prepared.ID)
			}
		}
	}
	return resp, nil
}

func (r *Runtime) Sessions() ([]app.SessionRecord, error) {
	if r.sessions == nil {
		return []app.SessionRecord{}, nil
	}
	return r.sessions.List()
}

func (r *Runtime) Session(id string) (app.SessionRecord, error) {
	if r.sessions == nil {
		return app.SessionRecord{}, errors.New("session store 未启用")
	}
	return r.sessions.Get(id)
}

func (r *Runtime) DeleteSession(id string) error {
	if r.sessions == nil {
		return errors.New("session store 未启用")
	}
	return r.sessions.Delete(id)
}

func (r *Runtime) AdvanceWorkflow(_ context.Context, req app.WorkflowAdvanceRequest) (app.WorkflowAdvanceResponse, error) {
	if r.sessions == nil {
		return app.WorkflowAdvanceResponse{}, errors.New("session store 未启用")
	}
	record, err := r.sessions.Get(req.SessionID)
	if err != nil {
		return app.WorkflowAdvanceResponse{}, err
	}
	next := findWorkflowNode(record.Workflow.Nodes, req.NextNodeID)
	current := findWorkflowNode(record.Workflow.Nodes, record.Workflow.CurrentNodeID)
	if next.ID == "" && !req.Replay && workflowNextNodePending(record.Workflow, current, req.NextNodeID) {
		return app.WorkflowAdvanceResponse{
			Session:  record,
			Workflow: record.Workflow,
			Node:     current,
			Ready:    false,
			Waiting:  true,
			Message:  "下一幕仍在准备文本和语音，请稍后继续。",
		}, nil
	}
	if next.ID != "" && !req.Replay && workflowNodeHasError(next) {
		message := strings.TrimSpace(next.PrepareError)
		if message == "" {
			message = "下一幕生成失败，请检查 agent 或语音 provider 配置。"
		}
		return app.WorkflowAdvanceResponse{
			Session:  record,
			Workflow: record.Workflow,
			Node:     current,
			Ready:    false,
			Waiting:  false,
			Message:  message,
		}, nil
	}
	if next.ID != "" && !req.Replay && !workflowNodeIsReady(next) {
		return app.WorkflowAdvanceResponse{
			Session:  record,
			Workflow: record.Workflow,
			Node:     current,
			Ready:    false,
			Waiting:  true,
			Message:  "下一幕仍在准备语音，请稍后继续。",
		}, nil
	}
	record, err = r.sessions.AdvanceWorkflow(req)
	if err != nil {
		return app.WorkflowAdvanceResponse{}, err
	}
	node := app.TeachingWorkflowNode{}
	for _, item := range record.Workflow.Nodes {
		if item.ID == record.Workflow.CurrentNodeID {
			node = item
			break
		}
	}
	if shouldQueueNextAct(node) {
		r.preloadRemainingWorkflowNodes(app.SceneGenerateRequest{
			Topic:        record.Teaching.Topic,
			DocumentText: record.Teaching.DocumentText,
			LearningGoal: record.Teaching.LearningGoal,
			Prompt:       record.Teaching.Prompt,
			Characters:   record.Characters,
			Runtime:      record.Teaching.Runtime,
			Variables:    record.Teaching.Variables,
		}, record.Session.ID, node.ID)
	}
	return app.WorkflowAdvanceResponse{
		Session:  record,
		Workflow: record.Workflow,
		Node:     node,
		Ready:    true,
	}, nil
}

func (r *Runtime) Turn(ctx context.Context, req app.TurnRequest) (app.TurnResponse, error) {
	character, err := resolveSingleCharacter(req)
	if err != nil {
		return app.TurnResponse{}, err
	}
	req.Character = character
	if req.User.UserID == "" {
		return app.TurnResponse{}, errors.New("user.user_id 不能为空")
	}
	if strings.TrimSpace(req.User.Text) == "" {
		return app.TurnResponse{}, errors.New("user.text 不能为空")
	}

	agentEngine, err := r.agent(req.Runtime.AgentProvider)
	if err != nil {
		return app.TurnResponse{}, err
	}
	agentOut, err := agentEngine.Discuss(ctx, agent.DiscussInput{Turn: req})
	if err != nil {
		return app.TurnResponse{}, err
	}
	agentOut = normalizeAgent(agentOut, character)
	if sanitized, marker := sanitizeWorkflowLeak(agentOut); marker != "" {
		r.logger.Warn("清理 agent 工作流上下文污染", "marker", marker, "session_id", req.Session.ID, "character_id", character.ID)
		agentOut = sanitized
	}

	voiceEngine, err := r.voice(req.Runtime.VoiceProvider)
	if err != nil {
		return app.TurnResponse{}, err
	}
	audio, err := r.synthesizeWithCache(ctx, req.Runtime.VoiceProvider, voiceEngine, voice.Input{
		Text:      agentOut.SpeechText,
		Plan:      agentOut.Voice,
		Emotion:   agentOut.Emotion,
		Character: character,
		Profile:   req.Runtime.Voice,
	})
	if err != nil {
		return app.TurnResponse{}, err
	}

	sceneImage, err := r.generateSceneImage(ctx, req, character)
	if err != nil {
		r.logger.Warn("生成场景 CG 失败", "error", err, "session_id", req.Session.ID, "character_id", character.ID)
		sceneImage = app.ImageResult{
			Prompt:      req.Runtime.Image.Prompt,
			Error:       err.Error(),
			Placeholder: true,
		}
	}

	resp := app.TurnResponse{
		DisplayText:  agentOut.DisplayText,
		SpeechText:   agentOut.SpeechText,
		Segments:     agentOut.Segments,
		Emotion:      agentOut.Emotion,
		Expression:   agentOut.Expression,
		Motion:       agentOut.Motion,
		Voice:        agentOut.Voice,
		MemoryWrites: agentOut.MemoryWrites,
		Audio:        audio,
		SceneImage:   sceneImage,
	}
	if r.sessions != nil {
		if _, err := r.sessions.AppendTurn(req, resp); err != nil {
			r.logger.Warn("写入会话历史失败", "error", err)
		}
	}
	return resp, nil
}

func (r *Runtime) SynthesizeVoice(ctx context.Context, req app.VoiceSynthesisRequest) (app.AudioResult, error) {
	if req.Text == "" {
		return app.AudioResult{}, errors.New("text 不能为空")
	}
	engine, err := r.voice(req.Provider)
	if err != nil {
		return app.AudioResult{}, err
	}
	if req.Plan.VoiceID == "" {
		req.Plan.VoiceID = req.Character.VoiceID
	}
	audio, err := r.synthesizeWithCache(ctx, req.Provider, engine, voice.Input{
		Text:      req.Text,
		Plan:      req.Plan,
		Emotion:   req.Emotion,
		Character: req.Character,
		Profile:   req.Profile,
	})
	if err != nil {
		return app.AudioResult{}, err
	}
	if r.sessions != nil && req.SessionID != "" && req.WorkflowNodeID != "" && audio.URL != "" {
		if _, err := r.sessions.AttachWorkflowAudio(req.SessionID, req.WorkflowNodeID, audio); err != nil {
			r.logger.Warn("写入工作流节点语音缓存失败", "error", err, "session_id", req.SessionID, "node_id", req.WorkflowNodeID)
		}
	}
	return audio, nil
}

func (r *Runtime) CloneVoice(ctx context.Context, req app.VoiceCloneRequest) (app.VoiceCloneResult, error) {
	provider := req.Provider
	if provider == "" {
		provider = string(voice.ProviderVolcengine)
	}
	engine, err := r.voice(provider)
	if err != nil {
		return app.VoiceCloneResult{}, err
	}
	trainer, ok := engine.(voice.CloneTrainer)
	if !ok {
		return app.VoiceCloneResult{}, fmt.Errorf("voice provider 不支持声音复刻: %s", provider)
	}
	req.Provider = provider
	return trainer.CloneVoice(ctx, req)
}

func (r *Runtime) CloneVoiceStatus(ctx context.Context, req app.VoiceCloneRequest) (app.VoiceCloneResult, error) {
	provider := req.Provider
	if provider == "" {
		provider = string(voice.ProviderVolcengine)
	}
	engine, err := r.voice(provider)
	if err != nil {
		return app.VoiceCloneResult{}, err
	}
	trainer, ok := engine.(voice.CloneTrainer)
	if !ok {
		return app.VoiceCloneResult{}, fmt.Errorf("voice provider 不支持声音复刻状态查询: %s", provider)
	}
	req.Provider = provider
	return trainer.CloneStatus(ctx, req)
}

func (r *Runtime) agent(requested string) (agent.Engine, error) {
	provider := agent.Provider(requested)
	if provider == "" {
		provider = r.defaultAgent
	}
	engine, ok := r.agents[provider]
	if !ok || engine == nil {
		return nil, fmt.Errorf("agent provider 不可用: %s", provider)
	}
	return engine, nil
}

func (r *Runtime) fillWorkflowDialogue(ctx context.Context, req app.SceneGenerateRequest, resp *app.SceneGenerateResponse) {
	ag, err := r.agent(req.Runtime.AgentProvider)
	if err != nil {
		return
	}
	character := app.Character{}
	if len(req.Characters) > 0 {
		character = req.Characters[0]
	}
	for i := range resp.Workflow.Nodes {
		node := &resp.Workflow.Nodes[i]
		if node.Kind != "lesson" && node.Kind != "opening" {
			continue
		}
		if len(node.Lines) > 0 || node.Line != "" {
			continue
		}
		summary := node.Summary
		if summary == "" {
			summary = node.Title
		}
		out, err := ag.Discuss(ctx, agent.DiscussInput{
			Turn: app.TurnRequest{
				Character: character,
				Scene:     app.Scene{Title: summary},
				Relation:  app.Relationship{UserID: "default"},
				User:      app.UserInput{UserID: "default", Text: "讲解「" + summary + "」。用自然对话口吻解释。"},
				Prompt:    req.Prompt,
				Runtime:   req.Runtime,
			},
		})
		if err != nil {
			continue
		}
		node.Lines = []app.DialogueLine{
			{Speaker: node.Speaker, Text: out.DisplayText, SpeechText: out.SpeechText, Expression: out.Expression},
		}
		node.Line = out.DisplayText
		node.SpeechText = out.SpeechText
	}
}

func (r *Runtime) voice(requested string) (voice.Engine, error) {
	provider := voice.Provider(requested)
	if provider == "" {
		provider = r.defaultVoice
	}
	engine, ok := r.voices[provider]
	if !ok || engine == nil {
		return nil, fmt.Errorf("voice provider 不可用: %s", provider)
	}
	return engine, nil
}

func (r *Runtime) synthesizeWithCache(ctx context.Context, provider string, engine voice.Engine, input voice.Input) (app.AudioResult, error) {
	key, err := voiceCacheKey(provider, input)
	if err != nil {
		return app.AudioResult{}, err
	}
	if cached, ok := r.cachedAudio(key); ok {
		cached.Cached = true
		return cached, nil
	}
	audio, err := engine.Synthesize(ctx, input)
	if err != nil {
		return app.AudioResult{}, err
	}
	if !audio.Placeholder && audio.URL != "" {
		r.storeCachedAudio(key, audio)
	}
	return audio, nil
}

func (r *Runtime) cachedAudio(key string) (app.AudioResult, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	audio, ok := r.voiceCache[key]
	return audio, ok
}

func (r *Runtime) storeCachedAudio(key string, audio app.AudioResult) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.voiceCache[key] = audio
}

func voiceCacheKey(provider string, input voice.Input) (string, error) {
	body, err := json.Marshal(struct {
		Provider  string           `json:"provider"`
		Text      string           `json:"text"`
		Plan      app.VoicePlan    `json:"plan"`
		Emotion   string           `json:"emotion"`
		Character app.Character    `json:"character"`
		Profile   app.VoiceProfile `json:"profile"`
	}{
		Provider:  provider,
		Text:      strings.TrimSpace(input.Text),
		Plan:      input.Plan,
		Emotion:   input.Emotion,
		Character: input.Character,
		Profile:   input.Profile,
	})
	if err != nil {
		return "", fmt.Errorf("生成语音缓存 key 失败: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (r *Runtime) image(requested string) (image.Engine, error) {
	provider := image.Provider(requested)
	if provider == "" {
		provider = r.defaultImage
	}
	engine, ok := r.images[provider]
	if !ok || engine == nil {
		return nil, fmt.Errorf("image provider 不可用: %s", provider)
	}
	return engine, nil
}

func (r *Runtime) scene(requested string) (scene.Engine, error) {
	provider := scene.Provider(requested)
	if provider == "" {
		provider = r.defaultScene
	}
	engine, ok := r.scenes[provider]
	if !ok || engine == nil {
		return nil, fmt.Errorf("scene provider 不可用: %s", provider)
	}
	return engine, nil
}

func (r *Runtime) generateSceneImage(ctx context.Context, req app.TurnRequest, character app.Character) (app.ImageResult, error) {
	if !req.Runtime.Image.Enabled {
		return app.ImageResult{}, nil
	}
	engine, err := r.image(req.Runtime.ImageProvider)
	if err != nil {
		return app.ImageResult{}, err
	}
	return engine.Generate(ctx, image.Input{
		Request:   req.Runtime.Image,
		Turn:      req,
		Character: character,
	})
}

func validateSceneGenerateRequest(req app.SceneGenerateRequest) error {
	if strings.TrimSpace(req.DocumentText) == "" && documentSource(req.Variables) == "" {
		return errors.New("document_text、document_url 或 document_asset 不能为空")
	}
	if len(req.Characters) != 1 {
		return fmt.Errorf("当前阶段每个教学场景只支持 1 个角色，收到 %d 个", len(req.Characters))
	}
	if strings.TrimSpace(req.Characters[0].ID) == "" {
		return errors.New("characters[0].id 不能为空")
	}
	return nil
}

func documentSource(variables map[string]string) string {
	for _, key := range []string{"document_url", "source_url", "document_asset_path", "material_file_path"} {
		if value := strings.TrimSpace(variables[key]); value != "" {
			return value
		}
	}
	return ""
}

func resolveSingleCharacter(req app.TurnRequest) (app.Character, error) {
	if len(req.Characters) > 1 {
		return app.Character{}, fmt.Errorf("当前阶段每次对话只支持 1 个角色，收到 %d 个", len(req.Characters))
	}

	character := req.Character
	if len(req.Characters) == 1 {
		character = req.Characters[0]
		if req.Character.ID != "" && req.Character.ID != character.ID {
			return app.Character{}, fmt.Errorf("character.id 与 characters[0].id 不一致: %s != %s", req.Character.ID, character.ID)
		}
	}
	if strings.TrimSpace(character.ID) == "" {
		return app.Character{}, errors.New("character.id 不能为空")
	}
	if req.Session.ActiveCharacterID != "" && req.Session.ActiveCharacterID != character.ID {
		return app.Character{}, fmt.Errorf("session.active_character_id 与 character.id 不一致: %s != %s", req.Session.ActiveCharacterID, character.ID)
	}
	if len(req.Session.ParticipantIDs) > 1 {
		return app.Character{}, fmt.Errorf("当前阶段 participant_ids 只支持 1 个角色，收到 %d 个", len(req.Session.ParticipantIDs))
	}
	if len(req.Session.ParticipantIDs) == 1 && req.Session.ParticipantIDs[0] != character.ID {
		return app.Character{}, fmt.Errorf("session.participant_ids[0] 与 character.id 不一致: %s != %s", req.Session.ParticipantIDs[0], character.ID)
	}
	return character, nil
}

func normalizeAgent(out agent.Output, character app.Character) agent.Output {
	if out.SpeechText == "" {
		out.SpeechText = out.DisplayText
	}
	if out.DisplayText == "" {
		out.DisplayText = out.SpeechText
	}
	if out.Emotion == "" {
		out.Emotion = "calm"
	}
	if out.Expression == "" {
		out.Expression = "soft_smile"
	}
	if out.Motion == "" {
		out.Motion = "idle"
	}
	if out.Voice.VoiceID == "" {
		out.Voice.VoiceID = character.VoiceID
	}
	if out.Voice.Style == "" {
		out.Voice.Style = "natural"
	}
	if out.Voice.Speed == 0 {
		out.Voice.Speed = 1
	}
	if out.Voice.Pitch == 0 {
		out.Voice.Pitch = 1
	}
	if out.MemoryWrites == nil {
		out.MemoryWrites = []app.MemoryWrite{}
	}
	out.Segments = normalizeSegments(out)
	return out
}

func sanitizeWorkflowLeak(out agent.Output) (agent.Output, string) {
	text := out.DisplayText + "\n" + out.SpeechText
	for _, segment := range out.Segments {
		text += "\n" + segment.Text + "\n" + segment.SpeechText
	}
	for _, memory := range out.MemoryWrites {
		text += "\n" + memory.Content
	}
	for _, marker := range []string{
		"OpenSpec",
		"Superpowers",
		"Phase 0",
		"Phase 1",
		"复杂度判定",
		"执行路径",
		"AGENTS.md",
		"RTK",
	} {
		if strings.Contains(text, marker) {
			out.DisplayText = "我刚才有点说偏了。我们回到这份材料本身吧：你把想看的段落发给我，我会只围绕它讲一个知识点。"
			out.SpeechText = out.DisplayText
			out.Emotion = "calm"
			out.Expression = "soft_smile"
			out.Motion = "idle"
			out.Segments = []app.Segment{{
				Text:       out.DisplayText,
				SpeechText: out.SpeechText,
				Emotion:    out.Emotion,
				Expression: out.Expression,
				Motion:     out.Motion,
			}}
			out.MemoryWrites = []app.MemoryWrite{}
			return out, marker
		}
	}
	return out, ""
}

func normalizeSegments(out agent.Output) []app.Segment {
	if len(out.Segments) == 0 {
		return []app.Segment{{
			Text:       out.DisplayText,
			SpeechText: out.SpeechText,
			Emotion:    out.Emotion,
			Expression: out.Expression,
			Motion:     out.Motion,
		}}
	}
	segments := make([]app.Segment, 0, len(out.Segments))
	for _, segment := range out.Segments {
		if strings.TrimSpace(segment.Text) == "" && strings.TrimSpace(segment.SpeechText) == "" {
			continue
		}
		if segment.Text == "" {
			segment.Text = out.DisplayText
		}
		if segment.SpeechText == "" {
			segment.SpeechText = out.SpeechText
		}
		if segment.Emotion == "" {
			segment.Emotion = out.Emotion
		}
		if segment.Expression == "" {
			segment.Expression = out.Expression
		}
		if segment.Motion == "" {
			segment.Motion = out.Motion
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return []app.Segment{{
			Text:       out.DisplayText,
			SpeechText: out.SpeechText,
			Emotion:    out.Emotion,
			Expression: out.Expression,
			Motion:     out.Motion,
		}}
	}
	return segments
}
