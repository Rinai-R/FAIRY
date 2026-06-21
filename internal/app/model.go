package app

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type Character struct {
	ID          string          `json:"id"`
	DisplayName string          `json:"display_name"`
	VoiceID     string          `json:"voice_id"`
	AvatarURL   string          `json:"avatar_url,omitempty"`
	Assets      CharacterAssets `json:"assets,omitempty"`
	Persona     string          `json:"persona"`
	StyleRules  []string        `json:"style_rules"`
	Prompt      PromptConfig    `json:"prompt,omitempty"`
	Runtime     RuntimeConfig   `json:"runtime,omitempty"`
}

type CharacterAssets struct {
	PortraitURL       string                   `json:"portrait_url,omitempty"`
	BackgroundURL     string                   `json:"background_url,omitempty"`
	Backgrounds       map[string]string        `json:"backgrounds,omitempty"`
	ReferenceImageURL string                   `json:"reference_image_url,omitempty"`
	StylePrompt       string                   `json:"style_prompt,omitempty"`
	CGPrompt          string                   `json:"cg_prompt,omitempty"`
	Moods             map[string]CharacterMood `json:"moods,omitempty"`
}

type CharacterMood struct {
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	PortraitURL string `json:"portrait_url,omitempty"`
	CGPrompt    string `json:"cg_prompt,omitempty"`
	VoiceStyle  string `json:"voice_style,omitempty"`
}

type ExpressionOption struct {
	Key           string `json:"key"`
	Label         string `json:"label,omitempty"`
	Description   string `json:"description,omitempty"`
	PortraitURL   string `json:"portrait_url,omitempty"`
	HasPortrait   bool   `json:"has_portrait,omitempty"`
	IsDefault     bool   `json:"is_default,omitempty"`
	DefaultReason string `json:"default_reason,omitempty"`
}

func ExpressionOptionsForCharacter(character Character) []ExpressionOption {
	options := []ExpressionOption{
		{Key: "soft_smile", Label: "默认微笑", IsDefault: true, DefaultReason: "没有更精确差分时的默认表情"},
		{Key: "thinking", Label: "思考", IsDefault: true, DefaultReason: "解释概念或推理时的默认表情"},
		{Key: "curious", Label: "好奇", IsDefault: true, DefaultReason: "追问或引出选择时的默认表情"},
		{Key: "calm", Label: "平静", IsDefault: true, DefaultReason: "总结或稳定讲解时的默认表情"},
		{Key: "serious", Label: "认真", IsDefault: true, DefaultReason: "纠错或强调边界时的默认表情"},
	}
	for key, mood := range character.Assets.Moods {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		options = append(options, ExpressionOption{
			Key:         key,
			Label:       mood.Label,
			Description: firstNonEmptyString(mood.Description, mood.CGPrompt, mood.VoiceStyle),
			PortraitURL: mood.PortraitURL,
			HasPortrait: strings.TrimSpace(mood.PortraitURL) != "",
		})
	}
	return sortedExpressionOptions(dedupeExpressionOptions(options))
}

func dedupeExpressionOptions(options []ExpressionOption) []ExpressionOption {
	seen := map[string]bool{}
	out := make([]ExpressionOption, 0, len(options))
	for _, option := range options {
		key := strings.TrimSpace(option.Key)
		if key == "" || seen[key] {
			continue
		}
		option.Key = key
		out = append(out, option)
		seen[key] = true
	}
	return out
}

func sortedExpressionOptions(options []ExpressionOption) []ExpressionOption {
	out := dedupeExpressionOptions(options)
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return !out[i].IsDefault
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

const (
	WorkflowNodeStatusPending      = "pending"
	WorkflowNodeStatusSynthesizing = "synthesizing"
	WorkflowNodeStatusReady        = "ready"
	WorkflowNodeStatusError        = "error"

	DialogueAudioStatusPending = "pending"
	DialogueAudioStatusReady   = "ready"
	DialogueAudioStatusError   = "error"

	SceneGenerationStatusGenerating = "generating"
	SceneGenerationStatusPreparing  = "preparing"
	SceneGenerationStatusReady      = "ready"
	SceneGenerationStatusFailed     = "failed"
)

type Session struct {
	ID                string   `json:"id"`
	UserID            string   `json:"user_id"`
	ActiveCharacterID string   `json:"active_character_id"`
	ParticipantIDs    []string `json:"participant_ids,omitempty"`
}

type SessionRecord struct {
	Session     Session          `json:"session"`
	Scene       Scene            `json:"scene"`
	Teaching    TeachingSnapshot `json:"teaching,omitempty"`
	Characters  []Character      `json:"characters,omitempty"`
	Interaction SceneInteraction `json:"interaction,omitempty"`
	Workflow    TeachingWorkflow `json:"workflow,omitempty"`
	Relation    Relationship     `json:"relation"`
	Messages    []Message        `json:"messages,omitempty"`
	Generation  SceneGeneration  `json:"generation,omitempty"`
	Events      []RuntimeEvent   `json:"events,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

const (
	RuntimeEventLevelInfo  = "info"
	RuntimeEventLevelWarn  = "warn"
	RuntimeEventLevelError = "error"

	RuntimeEventStageGeneration = "generation"
	RuntimeEventStageMaterial   = "material"
	RuntimeEventStageWorkflow   = "workflow"
	RuntimeEventStageVoice      = "voice"
	RuntimeEventStageAgent      = "agent"
	RuntimeEventStageImage      = "image"
	RuntimeEventStagePersist    = "persist"

	RuntimeEventTypeGenerationCreated      = "generation.created"
	RuntimeEventTypeGenerationComplete     = "generation.completed"
	RuntimeEventTypeGenerationFailed       = "generation.failed"
	RuntimeEventTypeMaterialPrepared       = "material.prepared"
	RuntimeEventTypeWorkflowNodeComplete   = "workflow.node.completed"
	RuntimeEventTypeWorkflowNodeFailed     = "workflow.node.failed"
	RuntimeEventTypeWorkflowAdvanceFailed  = "workflow.advance.failed"
	RuntimeEventTypeVoiceSynthesizeDone    = "voice.synthesize.completed"
	RuntimeEventTypeVoiceSynthesizeFailed  = "voice.synthesize.failed"
	RuntimeEventTypeAgentGenerateActDone   = "agent.generate_act.completed"
	RuntimeEventTypeAgentGenerateActFailed = "agent.generate_act.failed"
	RuntimeEventTypeAgentGenerateActRetry  = "agent.generate_act.retry"
	RuntimeEventTypeAgentActPlanDone       = "agent.actplan.completed"
	RuntimeEventTypeAgentActPlanCacheHit   = "agent.actplan.cache_hit"
	RuntimeEventTypeAgentActPlanFailed     = "agent.actplan.failed"
	RuntimeEventTypeAgentActPlanRetry      = "agent.actplan.retry"
	RuntimeEventTypeAgentDraftDone         = "agent.generate_act.draft.completed"
	RuntimeEventTypeAgentDraftFailed       = "agent.generate_act.draft.failed"
	RuntimeEventTypeAgentDraftRetry        = "agent.generate_act.draft.retry"
	RuntimeEventTypeAgentRewriteDone       = "agent.rewrite_act.completed"
	RuntimeEventTypeAgentRewriteFailed     = "agent.rewrite_act.failed"
	RuntimeEventTypeAgentRewriteRetry      = "agent.rewrite_act.retry"
	RuntimeEventTypeImageGenerateDone      = "image.generate.completed"
	RuntimeEventTypeImageGenerateFailed    = "image.generate.failed"
	RuntimeEventTypePersistWorkflowSaved   = "persist.workflow.saved"
	RuntimeEventTypePersistWorkflowFailed  = "persist.workflow.failed"
	RuntimeEventTypePersistTurnSaved       = "persist.turn.saved"
	RuntimeEventTypePersistTurnFailed      = "persist.turn.failed"
)

type RuntimeEvent struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Level      string    `json:"level"`
	Type       string    `json:"type"`
	Stage      string    `json:"stage"`
	Message    string    `json:"message"`
	Detail     string    `json:"detail,omitempty"`
	NodeID     string    `json:"node_id,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	RetryCount int       `json:"retry_count,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type RuntimeEventListResponse struct {
	Events []RuntimeEvent `json:"events"`
}

type SceneGeneration struct {
	Status      string               `json:"status,omitempty"`
	Fingerprint string               `json:"fingerprint,omitempty"`
	Request     SceneGenerateRequest `json:"request,omitempty"`
	Error       string               `json:"error,omitempty"`
	StartedAt   time.Time            `json:"started_at,omitempty"`
	CompletedAt time.Time            `json:"completed_at,omitempty"`
}

type TeachingSnapshot struct {
	Topic           string            `json:"topic,omitempty"`
	LearningGoal    string            `json:"learning_goal,omitempty"`
	Prompt          PromptConfig      `json:"prompt,omitempty"`
	Runtime         RuntimeConfig     `json:"runtime,omitempty"`
	MaterialSource  MaterialSource    `json:"material_source,omitempty"`
	MaterialContext MaterialContext   `json:"material_context,omitempty"`
	Variables       map[string]string `json:"variables,omitempty"`
}

type Message struct {
	ID               string    `json:"id"`
	SessionID        string    `json:"session_id"`
	Role             string    `json:"role"`
	CharacterID      string    `json:"character_id,omitempty"`
	Text             string    `json:"text"`
	DisplayText      string    `json:"display_text,omitempty"`
	SpeechText       string    `json:"speech_text,omitempty"`
	Segments         []Segment `json:"segments,omitempty"`
	Emotion          string    `json:"emotion,omitempty"`
	Expression       string    `json:"expression,omitempty"`
	Motion           string    `json:"motion,omitempty"`
	AudioURL         string    `json:"audio_url,omitempty"`
	SceneImageURL    string    `json:"scene_image_url,omitempty"`
	SceneImagePrompt string    `json:"scene_image_prompt,omitempty"`
	SceneImageError  string    `json:"scene_image_error,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type Scene struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Location     string            `json:"location"`
	Phase        string            `json:"phase"`
	Variables    map[string]string `json:"variables,omitempty"`
	LastActiveAt time.Time         `json:"last_active_at"`
}

type Relationship struct {
	UserID    string    `json:"user_id"`
	Affinity  float64   `json:"affinity"`
	Trust     float64   `json:"trust"`
	Tension   float64   `json:"tension"`
	Closeness float64   `json:"closeness"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TurnRequest struct {
	Session    Session       `json:"session,omitempty"`
	Characters []Character   `json:"characters,omitempty"`
	Character  Character     `json:"character,omitempty"`
	Scene      Scene         `json:"scene"`
	Relation   Relationship  `json:"relation"`
	User       UserInput     `json:"user"`
	Prompt     PromptConfig  `json:"prompt,omitempty"`
	Runtime    RuntimeConfig `json:"runtime,omitempty"`
}

type UserInput struct {
	UserID string `json:"user_id"`
	Text   string `json:"text"`
	Mode   string `json:"mode,omitempty"`
}

type TurnResponse struct {
	DisplayText  string        `json:"display_text,omitempty"`
	SpeechText   string        `json:"speech_text,omitempty"`
	Segments     []Segment     `json:"segments,omitempty"`
	Emotion      string        `json:"emotion"`
	Expression   string        `json:"expression"`
	Motion       string        `json:"motion"`
	Voice        VoicePlan     `json:"voice"`
	MemoryWrites []MemoryWrite `json:"memory_writes,omitempty"`
	Audio        AudioResult   `json:"audio"`
	SceneImage   ImageResult   `json:"scene_image,omitempty"`
}

type Segment struct {
	Text       string `json:"text"`
	SpeechText string `json:"speech_text,omitempty"`
	Emotion    string `json:"emotion,omitempty"`
	Expression string `json:"expression,omitempty"`
	Motion     string `json:"motion,omitempty"`
}

type VoiceSynthesisRequest struct {
	Provider       string       `json:"provider,omitempty"`
	Text           string       `json:"text"`
	Plan           VoicePlan    `json:"plan,omitempty"`
	Emotion        string       `json:"emotion,omitempty"`
	Character      Character    `json:"character,omitempty"`
	Profile        VoiceProfile `json:"profile,omitempty"`
	SessionID      string       `json:"session_id,omitempty"`
	WorkflowNodeID string       `json:"workflow_node_id,omitempty"`
}

type VoicePlan struct {
	VoiceID string  `json:"voice_id"`
	Style   string  `json:"style"`
	Speed   float64 `json:"speed"`
	Pitch   float64 `json:"pitch"`
}

type AudioResult struct {
	URL         string `json:"url,omitempty"`
	Format      string `json:"format"`
	DurationMS  int    `json:"duration_ms"`
	Placeholder bool   `json:"placeholder"`
	Cached      bool   `json:"cached,omitempty"`
}

type PromptConfig struct {
	System           string   `json:"system,omitempty"`
	Developer        string   `json:"developer,omitempty"`
	SceneInstruction string   `json:"scene_instruction,omitempty"`
	ResponseContract string   `json:"response_contract,omitempty"`
	StyleRules       []string `json:"style_rules,omitempty"`
}

type RuntimeConfig struct {
	AgentProvider string       `json:"agent_provider,omitempty"`
	VoiceProvider string       `json:"voice_provider,omitempty"`
	ImageProvider string       `json:"image_provider,omitempty"`
	SceneProvider string       `json:"scene_provider,omitempty"`
	Agent         AgentProfile `json:"agent,omitempty"`
	Voice         VoiceProfile `json:"voice,omitempty"`
	Image         ImageRequest `json:"image,omitempty"`
	Language      LanguagePlan `json:"language,omitempty"`
}

type AgentProfile struct {
	Endpoint  string `json:"endpoint,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	ExtraBody string `json:"extra_body,omitempty"`
}

type LanguagePlan struct {
	DisplayLanguage     string `json:"display_language,omitempty"`
	SpeechLanguage      string `json:"speech_language,omitempty"`
	TranslationProvider string `json:"translation_provider,omitempty"`
	Mode                string `json:"mode,omitempty"`
}

const (
	DefaultDisplayLanguage     = "zh-CN"
	DefaultTranslationProvider = "agent"
	DefaultLanguageMode        = "translate_for_voice"
)

func (plan LanguagePlan) Normalize() LanguagePlan {
	displayLanguage := NormalizeLanguageCode(plan.DisplayLanguage)
	if displayLanguage == "" {
		displayLanguage = DefaultDisplayLanguage
	}
	mode := strings.TrimSpace(plan.Mode)
	if mode == "" {
		mode = DefaultLanguageMode
	}
	speechLanguage := NormalizeLanguageCode(plan.SpeechLanguage)
	if speechLanguage == "" {
		speechLanguage = displayLanguage
	}
	translationProvider := strings.TrimSpace(plan.TranslationProvider)
	if translationProvider == "" {
		translationProvider = DefaultTranslationProvider
	}
	if mode == "same" {
		speechLanguage = displayLanguage
	}
	return LanguagePlan{
		DisplayLanguage:     displayLanguage,
		SpeechLanguage:      speechLanguage,
		TranslationProvider: translationProvider,
		Mode:                mode,
	}
}

func NormalizeLanguageCode(language string) string {
	value := strings.TrimSpace(language)
	switch strings.ToLower(strings.ReplaceAll(value, "_", "-")) {
	case "":
		return ""
	case "cn", "zh", "zh-cn", "zh-hans", "zh-hans-cn":
		return "zh-CN"
	case "jp", "ja", "ja-jp":
		return "ja-JP"
	case "en", "en-us":
		return "en-US"
	default:
		return value
	}
}

func IsChineseLanguage(language string) bool {
	return NormalizeLanguageCode(language) == "zh-CN"
}

func IsJapaneseLanguage(language string) bool {
	return NormalizeLanguageCode(language) == "ja-JP"
}

func IsEnglishLanguage(language string) bool {
	return NormalizeLanguageCode(language) == "en-US"
}

type SceneGenerateRequest struct {
	Topic           string            `json:"topic,omitempty"`
	LearningGoal    string            `json:"learning_goal,omitempty"`
	InteractionMode string            `json:"interaction_mode,omitempty"`
	Prompt          PromptConfig      `json:"prompt,omitempty"`
	Characters      []Character       `json:"characters,omitempty"`
	Runtime         RuntimeConfig     `json:"runtime,omitempty"`
	MaterialSource  MaterialSource    `json:"material_source,omitempty"`
	MaterialContext MaterialContext   `json:"material_context,omitempty"`
	Variables       map[string]string `json:"variables,omitempty"`
}

type MaterialSourceMode string

const (
	MaterialSourceText         MaterialSourceMode = "text"
	MaterialSourceUploadedFile MaterialSourceMode = "uploaded_file"
)

type MaterialSource struct {
	Mode      MaterialSourceMode `json:"mode,omitempty"`
	Text      string             `json:"text,omitempty"`
	AssetID   string             `json:"asset_id,omitempty"`
	AssetName string             `json:"asset_name,omitempty"`
	AssetType string             `json:"asset_type,omitempty"`
	AssetPath string             `json:"asset_path,omitempty"`
}

type MaterialContext struct {
	Brief  string               `json:"brief,omitempty"`
	Text   string               `json:"text,omitempty"`
	Report MaterialSourceReport `json:"report,omitempty"`
}

func SceneGenerateMaterialText(req SceneGenerateRequest) string {
	if text := strings.TrimSpace(req.MaterialContext.Text); text != "" {
		return text
	}
	if text := strings.TrimSpace(req.MaterialContext.Brief); text != "" {
		return text
	}
	if req.MaterialSource.Mode == MaterialSourceText {
		return strings.TrimSpace(req.MaterialSource.Text)
	}
	return ""
}

type MaterialSourceReport struct {
	Mode       MaterialSourceMode `json:"mode,omitempty"`
	Summary    string             `json:"summary,omitempty"`
	Items      []MaterialItem     `json:"items,omitempty"`
	TotalBytes int64              `json:"total_bytes,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
	Errors     []string           `json:"errors,omitempty"`
}

type MaterialItem struct {
	SourceType  string `json:"source_type,omitempty"`
	Path        string `json:"path,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Status      string `json:"status,omitempty"`
	Message     string `json:"message,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	TextBytes   int    `json:"text_bytes,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

type SceneGenerateResponse struct {
	Scene          Scene            `json:"scene"`
	Session        Session          `json:"session"`
	Relation       Relationship     `json:"relation"`
	OpeningMessage string           `json:"opening_message"`
	Interaction    SceneInteraction `json:"interaction,omitempty"`
	Workflow       TeachingWorkflow `json:"workflow,omitempty"`
	Image          ImageRequest     `json:"image,omitempty"`
	Prompt         PromptConfig     `json:"prompt,omitempty"`
}

type SceneGenerationStartResponse struct {
	Record    SessionRecord `json:"record"`
	Duplicate bool          `json:"duplicate,omitempty"`
}

type SceneInteraction struct {
	Mode    string        `json:"mode,omitempty"`
	Choices []SceneChoice `json:"choices,omitempty"`
}

type SceneChoice struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Text         string `json:"text"`
	Hint         string `json:"hint,omitempty"`
	TargetNodeID string `json:"target_node_id,omitempty"`
}

type DialogueLine struct {
	Speaker     string      `json:"speaker"`
	Text        string      `json:"text"`
	SpeechText  string      `json:"speech_text,omitempty"`
	Expression  string      `json:"expression,omitempty"`
	Audio       AudioResult `json:"audio,omitempty"`
	AudioStatus string      `json:"audio_status,omitempty"`
	AudioError  string      `json:"audio_error,omitempty"`
}

type TeachingWorkflow struct {
	ID            string                 `json:"id,omitempty"`
	Title         string                 `json:"title,omitempty"`
	Goal          string                 `json:"goal,omitempty"`
	CurrentNodeID string                 `json:"current_node_id,omitempty"`
	Preparing     bool                   `json:"preparing,omitempty"`
	PendingNodeID string                 `json:"pending_node_id,omitempty"`
	Nodes         []TeachingWorkflowNode `json:"nodes,omitempty"`
	History       []WorkflowHistoryItem  `json:"history,omitempty"`
}

type WorkflowHistoryItem struct {
	NodeID      string    `json:"node_id"`
	NodeTitle   string    `json:"node_title,omitempty"`
	NodeKind    string    `json:"node_kind,omitempty"`
	ChoiceID    string    `json:"choice_id,omitempty"`
	ChoiceLabel string    `json:"choice_label,omitempty"`
	Action      string    `json:"action,omitempty"`
	AudioURL    string    `json:"audio_url,omitempty"`
	AudioFormat string    `json:"audio_format,omitempty"`
	AudioCached bool      `json:"audio_cached,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

type TeachingWorkflowNode struct {
	ID                      string         `json:"id"`
	Kind                    string         `json:"kind"`
	Title                   string         `json:"title"`
	Decision                string         `json:"decision,omitempty"`
	Summary                 string         `json:"summary,omitempty"`
	TeachingGoal            string         `json:"teaching_goal,omitempty"`
	MustCover               []string       `json:"must_cover,omitempty"`
	MisconceptionToAddress  string         `json:"misconception_to_address,omitempty"`
	ExampleOrCounterexample string         `json:"example_or_counterexample,omitempty"`
	Speaker                 string         `json:"speaker,omitempty"`
	Line                    string         `json:"line,omitempty"`
	Lines                   []DialogueLine `json:"lines,omitempty"`
	SpeechText              string         `json:"speech_text,omitempty"`
	Challenge               string         `json:"challenge,omitempty"`
	BackgroundKey           string         `json:"background_key,omitempty"`
	BackgroundURL           string         `json:"background_url,omitempty"`
	Choices                 []SceneChoice  `json:"choices,omitempty"`
	NextNodeID              string         `json:"next_node_id,omitempty"`
	FreeDiscussion          bool           `json:"free_discussion,omitempty"`
	Status                  string         `json:"status,omitempty"`
	VoiceStatus             string         `json:"voice_status,omitempty"`
	ReadyAt                 time.Time      `json:"ready_at,omitempty"`
	PrepareError            string         `json:"prepare_error,omitempty"`
}

type WorkflowAdvanceRequest struct {
	SessionID     string `json:"session_id"`
	CurrentNodeID string `json:"current_node_id,omitempty"`
	NextNodeID    string `json:"next_node_id"`
	ChoiceID      string `json:"choice_id,omitempty"`
	Replay        bool   `json:"replay,omitempty"`
}

type WorkflowAdvanceResponse struct {
	Session  SessionRecord        `json:"session"`
	Workflow TeachingWorkflow     `json:"workflow"`
	Node     TeachingWorkflowNode `json:"node"`
	Ready    bool                 `json:"ready,omitempty"`
	Waiting  bool                 `json:"waiting,omitempty"`
	Message  string               `json:"message,omitempty"`
}

type WebGALExportRequest struct {
	Scene          Scene            `json:"scene"`
	Characters     []Character      `json:"characters"`
	Interaction    SceneInteraction `json:"interaction,omitempty"`
	Workflow       TeachingWorkflow `json:"workflow,omitempty"`
	OpeningMessage string           `json:"opening_message"`
	Image          ImageRequest     `json:"image,omitempty"`
}

type WebGALExportResponse struct {
	EntryFile string            `json:"entry_file"`
	Script    string            `json:"script"`
	Files     map[string]string `json:"files"`
}

type Capabilities struct {
	Providers      ProviderCatalog `json:"providers"`
	Defaults       RuntimeConfig   `json:"defaults"`
	Features       []string        `json:"features"`
	DesktopReady   bool            `json:"desktop_ready"`
	PluginManifest string          `json:"plugin_manifest,omitempty"`
}

type ProviderCatalog struct {
	Agents []ProviderInfo `json:"agents"`
	Voices []ProviderInfo `json:"voices"`
	Images []ProviderInfo `json:"images"`
	Scenes []ProviderInfo `json:"scenes"`
}

type ProviderInfo struct {
	ID          string            `json:"id"`
	Domain      string            `json:"domain"`
	DisplayName string            `json:"display_name"`
	Kind        string            `json:"kind"`
	Local       bool              `json:"local"`
	Streaming   bool              `json:"streaming"`
	Config      map[string]string `json:"config,omitempty"`
}

type PluginCatalog struct {
	Version   string           `json:"version"`
	Manifests []PluginManifest `json:"manifests"`
	Errors    []string         `json:"errors,omitempty"`
}

type DocumentUploadRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	DataBase64  string `json:"data_base64"`
}

type DocumentAsset struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
}

type PluginManifest struct {
	Path      string           `json:"path,omitempty"`
	Version   string           `json:"version"`
	Providers []PluginProvider `json:"providers"`
}

type PluginProvider struct {
	Domain          string `json:"domain"`
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	Description     string `json:"description,omitempty"`
	DefaultEndpoint string `json:"default_endpoint,omitempty"`
	Adapter         string `json:"adapter,omitempty"`
}

type VoiceProfile struct {
	Endpoint  string            `json:"endpoint,omitempty"`
	VoiceID   string            `json:"voice_id,omitempty"`
	TextLang  string            `json:"text_lang,omitempty"`
	MediaType string            `json:"media_type,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

type VoiceCloneSample struct {
	Filename   string `json:"filename,omitempty"`
	MimeType   string `json:"mime_type,omitempty"`
	Format     string `json:"format,omitempty"`
	DataBase64 string `json:"data_base64"`
	Transcript string `json:"transcript,omitempty"`
}

type VoiceCloneRequest struct {
	Provider    string             `json:"provider,omitempty"`
	AppID       string             `json:"app_id"`
	AccessToken string             `json:"access_token"`
	ResourceID  string             `json:"resource_id"`
	SpeakerID   string             `json:"speaker_id"`
	Language    string             `json:"language"`
	Samples     []VoiceCloneSample `json:"samples,omitempty"`
}

type VoiceCloneResult struct {
	SpeakerID   string   `json:"speaker_id"`
	ResourceID  string   `json:"resource_id"`
	Status      string   `json:"status,omitempty"`
	StatusCode  int      `json:"status_code,omitempty"`
	Message     string   `json:"message,omitempty"`
	LogID       string   `json:"log_id,omitempty"`
	SampleCount int      `json:"sample_count,omitempty"`
	SampleLogs  []string `json:"sample_logs,omitempty"`
}

type ImageRequest struct {
	Enabled           bool              `json:"enabled,omitempty"`
	Endpoint          string            `json:"endpoint,omitempty"`
	Prompt            string            `json:"prompt,omitempty"`
	NegativePrompt    string            `json:"negative_prompt,omitempty"`
	BackgroundURL     string            `json:"background_url,omitempty"`
	ReferenceImageURL string            `json:"reference_image_url,omitempty"`
	Workflow          json.RawMessage   `json:"workflow,omitempty"`
	Style             string            `json:"style,omitempty"`
	Size              string            `json:"size,omitempty"`
	Seed              int64             `json:"seed,omitempty"`
	Extra             map[string]string `json:"extra,omitempty"`
}

type ImageResult struct {
	URL         string `json:"url,omitempty"`
	Format      string `json:"format,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Error       string `json:"error,omitempty"`
	Placeholder bool   `json:"placeholder,omitempty"`
}

type MemoryWrite struct {
	Type       string   `json:"type"`
	Content    string   `json:"content"`
	Importance float64  `json:"importance"`
	Emotion    string   `json:"emotion,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

type MemoryItem struct {
	ID          string   `json:"id"`
	CharacterID string   `json:"character_id"`
	UserID      string   `json:"user_id"`
	Type        string   `json:"type"`
	Content     string   `json:"content"`
	Importance  float64  `json:"importance"`
	Emotion     string   `json:"emotion,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source,omitempty"`
}
