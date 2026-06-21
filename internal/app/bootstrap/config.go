package bootstrap

import (
	"log/slog"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	agentcodex "github.com/Rinai-R/FAIRY/internal/adapters/agent/codex"
	agentfairy "github.com/Rinai-R/FAIRY/internal/adapters/agent/fairy"
	agentmock "github.com/Rinai-R/FAIRY/internal/adapters/agent/mock"
	"github.com/Rinai-R/FAIRY/internal/adapters/image"
	imagecomfyui "github.com/Rinai-R/FAIRY/internal/adapters/image/comfyui"
	imagemock "github.com/Rinai-R/FAIRY/internal/adapters/image/mock"
	llmopenaicompatible "github.com/Rinai-R/FAIRY/internal/adapters/llm/openaicompatible"
	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	scenecodex "github.com/Rinai-R/FAIRY/internal/adapters/scene/codex"
	scenemock "github.com/Rinai-R/FAIRY/internal/adapters/scene/mock"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	voicevolcengine "github.com/Rinai-R/FAIRY/internal/adapters/voice/volcengine"
	"github.com/Rinai-R/FAIRY/internal/llm"
	"github.com/Rinai-R/FAIRY/internal/plugins"
	"github.com/Rinai-R/FAIRY/internal/runtime"
)

type Config struct {
	AgentProvider agent.Provider
	VoiceProvider voice.Provider
	ImageProvider image.Provider
	SceneProvider scene.Provider

	CodexBin            string
	CodexModel          string
	CodexWorkDir        string
	CodexTimeout        int
	FairyAgentEndpoint  string
	FairyAgentAPIKey    string
	FairyAgentModel     string
	FairyAgentExtraBody string
	FairyAgentTimeout   int
	SessionPath         string
	AppSessionPath      string
	UserConfigPath      string

	AudioDir     string
	ImageDir     string
	MaterialDir  string
	AudioBaseURL string
	ImageBaseURL string

	ComfyUIEndpoint string

	VolcengineEndpoint   string
	VolcengineAPIKey     string
	VolcengineResourceID string
	VolcengineSpeaker    string
	VolcengineFormat     string
	VolcengineUserID     string
	VolcengineSampleRate int

	PluginManifestPath string
	PluginDir          string
}

func DefaultConfig() Config {
	return Config{
		AgentProvider:        agent.ProviderMock,
		VoiceProvider:        voice.ProviderVolcengine,
		ImageProvider:        image.ProviderMock,
		SceneProvider:        scene.ProviderMock,
		CodexBin:             agentcodex.DefaultBin,
		CodexWorkDir:         agentcodex.DefaultWorkDir,
		CodexTimeout:         120,
		FairyAgentTimeout:    llmopenaicompatible.DefaultTimeout,
		SessionPath:          agentcodex.DefaultSessionPath,
		AppSessionPath:       "data/sessions.json",
		UserConfigPath:       "data/user-config.json",
		AudioDir:             "tmp/audio",
		ImageDir:             imagecomfyui.DefaultOutputDir,
		MaterialDir:          runtime.DefaultMaterialDir,
		AudioBaseURL:         "/audio/",
		ImageBaseURL:         imagecomfyui.DefaultBaseURL,
		ComfyUIEndpoint:      imagecomfyui.DefaultEndpoint,
		VolcengineEndpoint:   voicevolcengine.DefaultEndpoint,
		VolcengineResourceID: voicevolcengine.DefaultResourceID,
		VolcengineSpeaker:    voicevolcengine.DefaultSpeaker,
		VolcengineFormat:     voicevolcengine.DefaultFormat,
		VolcengineUserID:     voicevolcengine.DefaultUserID,
		VolcengineSampleRate: voicevolcengine.DefaultSampleRate,
		PluginManifestPath:   "configs/plugins.json",
		PluginDir:            "plugins",
	}
}

func NewRuntime(config Config, logger *slog.Logger) *runtime.Runtime {
	pluginCatalog := plugins.NewCatalog(config.PluginManifestPath, config.PluginDir)
	return runtime.NewRuntime(runtime.Dependencies{
		Agents: map[agent.Provider]agent.Engine{
			agent.ProviderMock:  agentmock.MockEngine{},
			agent.ProviderCodex: buildCodex(config, logger),
			agent.ProviderFairy: buildFairyAgent(config),
		},
		Voices: buildVoices(config),
		Images: map[image.Provider]image.Engine{
			image.ProviderMock:    imagemock.Engine{},
			image.ProviderComfyUI: buildComfyUI(config),
		},
		Scenes: map[scene.Provider]scene.Engine{
			scene.ProviderMock:  scenemock.Engine{},
			scene.ProviderCodex: buildCodexScene(config),
		},
		DefaultAgent: config.AgentProvider,
		DefaultVoice: config.VoiceProvider,
		DefaultImage: config.ImageProvider,
		DefaultScene: config.SceneProvider,
		MaterialDir:  config.MaterialDir,
		Sessions:     runtime.NewFileSessionStore(config.AppSessionPath),
		Plugins:      pluginCatalog,
		Logger:       logger,
	})
}

func buildFairyAgent(config Config) agent.Engine {
	return agentfairy.NewEngine(agentfairy.Options{
		Model: llmopenaicompatible.NewAdapter(llmopenaicompatible.Options{
			Profile: llm.Profile{
				Endpoint:  config.FairyAgentEndpoint,
				APIKey:    config.FairyAgentAPIKey,
				Model:     config.FairyAgentModel,
				ExtraBody: config.FairyAgentExtraBody,
			},
			TimeoutSec: config.FairyAgentTimeout,
		}),
	})
}

func buildVoices(config Config) map[voice.Provider]voice.Engine {
	return map[voice.Provider]voice.Engine{
		voice.ProviderVolcengine: buildVolcengine(config),
	}
}

func buildComfyUI(config Config) image.Engine {
	return imagecomfyui.NewEngine(imagecomfyui.Options{
		Endpoint:  config.ComfyUIEndpoint,
		OutputDir: config.ImageDir,
		BaseURL:   config.ImageBaseURL,
	})
}

func buildCodexScene(config Config) scene.Engine {
	return scenecodex.NewEngine(scenecodex.Options{
		CodexBin:     config.CodexBin,
		CodexModel:   config.CodexModel,
		CodexWorkDir: config.CodexWorkDir,
		CodexTimeout: config.CodexTimeout,
	})
}

func buildCodex(config Config, logger *slog.Logger) agent.Engine {
	return agentcodex.NewRuntime(agentcodex.Options{
		CodexBin:     config.CodexBin,
		CodexModel:   config.CodexModel,
		CodexWorkDir: config.CodexWorkDir,
		CodexTimeout: config.CodexTimeout,
		SessionPath:  config.SessionPath,
		Logger:       logger,
	})
}

func buildVolcengine(config Config) voice.Engine {
	return voicevolcengine.NewEngine(voicevolcengine.Options{
		Endpoint:   config.VolcengineEndpoint,
		APIKey:     config.VolcengineAPIKey,
		ResourceID: config.VolcengineResourceID,
		Speaker:    config.VolcengineSpeaker,
		Format:     config.VolcengineFormat,
		UserID:     config.VolcengineUserID,
		OutputDir:  config.AudioDir,
		BaseURL:    config.AudioBaseURL,
		SampleRate: config.VolcengineSampleRate,
	})
}
