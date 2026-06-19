package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/image"
	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
	"github.com/Rinai-R/FAIRY/internal/server"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
)

const (
	defaultHTTPAddr           = ":8787"
	defaultMaxRequestBodySize = 72 << 20
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	config, err := configFromEnv()
	if err != nil {
		logger.Error("FAIRY 配置无效", "error", err)
		os.Exit(1)
	}
	rt := bootstrap.NewRuntime(config, logger)

	addr := optionalString(os.LookupEnv, "FAIRY_ADDR", defaultHTTPAddr)
	maxRequestBodySize, err := optionalPositiveInt(os.LookupEnv, "FAIRY_MAX_REQUEST_BODY_BYTES", defaultMaxRequestBodySize)
	if err != nil {
		logger.Error("FAIRY HTTP 配置无效", "error", err)
		os.Exit(1)
	}
	h := hertzserver.Default(
		hertzserver.WithHostPorts(addr),
		hertzserver.WithMaxRequestBodySize(maxRequestBodySize),
	)
	server.Register(h, rt, server.Options{
		AudioDir:       config.AudioDir,
		ImageDir:       config.ImageDir,
		UserConfigPath: config.UserConfigPath,
		Logger:         logger,
	})

	go func() {
		logger.Info("FAIRY 服务已启动", "addr", addr)
		if err := h.Run(); err != nil {
			logger.Error("FAIRY 服务异常退出", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Shutdown(shutdownCtx); err != nil {
		logger.Error("FAIRY 服务停止失败", "error", err)
		os.Exit(1)
	}
}

func configFromEnv() (bootstrap.Config, error) {
	return configFromLookup(os.LookupEnv)
}

func configFromLookup(lookup func(string) (string, bool)) (bootstrap.Config, error) {
	config := bootstrap.DefaultConfig()

	agentProvider, err := parseAgentProvider(optionalString(lookup, "FAIRY_AGENT_ENGINE", string(config.AgentProvider)))
	if err != nil {
		return bootstrap.Config{}, err
	}
	voiceProvider, err := parseVoiceProvider(optionalString(lookup, "FAIRY_VOICE_ENGINE", string(config.VoiceProvider)))
	if err != nil {
		return bootstrap.Config{}, err
	}
	imageProvider, err := parseImageProvider(optionalString(lookup, "FAIRY_IMAGE_ENGINE", string(config.ImageProvider)))
	if err != nil {
		return bootstrap.Config{}, err
	}
	sceneProvider, err := parseSceneProvider(optionalString(lookup, "FAIRY_SCENE_ENGINE", string(config.SceneProvider)))
	if err != nil {
		return bootstrap.Config{}, err
	}
	codexTimeout, err := optionalPositiveInt(lookup, "FAIRY_CODEX_TIMEOUT_SECONDS", config.CodexTimeout)
	if err != nil {
		return bootstrap.Config{}, err
	}
	fairyAgentTimeout, err := optionalPositiveInt(lookup, "FAIRY_AGENT_TIMEOUT_SECONDS", config.FairyAgentTimeout)
	if err != nil {
		return bootstrap.Config{}, err
	}
	volcengineSampleRate, err := optionalPositiveInt(lookup, "FAIRY_VOLCENGINE_TTS_SAMPLE_RATE", config.VolcengineSampleRate)
	if err != nil {
		return bootstrap.Config{}, err
	}

	config.AgentProvider = agentProvider
	config.VoiceProvider = voiceProvider
	config.ImageProvider = imageProvider
	config.SceneProvider = sceneProvider
	config.CodexBin = optionalString(lookup, "FAIRY_CODEX_BIN", config.CodexBin)
	config.CodexModel = optionalString(lookup, "FAIRY_CODEX_MODEL", config.CodexModel)
	config.CodexWorkDir = optionalString(lookup, "FAIRY_CODEX_WORKDIR", config.CodexWorkDir)
	config.CodexTimeout = codexTimeout
	config.FairyAgentEndpoint = optionalString(lookup, "FAIRY_AGENT_ENDPOINT", config.FairyAgentEndpoint)
	config.FairyAgentAPIKey = optionalSecret(lookup, "FAIRY_AGENT_API_KEY", config.FairyAgentAPIKey)
	config.FairyAgentModel = optionalString(lookup, "FAIRY_AGENT_MODEL", config.FairyAgentModel)
	config.FairyAgentTimeout = fairyAgentTimeout
	config.SessionPath = optionalString(lookup, "FAIRY_CODEX_SESSION_PATH", config.SessionPath)
	config.AppSessionPath = optionalString(lookup, "FAIRY_SESSION_PATH", config.AppSessionPath)
	config.UserConfigPath = optionalString(lookup, "FAIRY_USER_CONFIG_PATH", config.UserConfigPath)
	config.AudioDir = optionalString(lookup, "FAIRY_AUDIO_DIR", config.AudioDir)
	config.ImageDir = optionalString(lookup, "FAIRY_IMAGE_DIR", config.ImageDir)
	config.MaterialDir = optionalString(lookup, "FAIRY_MATERIAL_DIR", config.MaterialDir)
	config.ImageBaseURL = optionalString(lookup, "FAIRY_IMAGE_BASE_URL", config.ImageBaseURL)
	config.MacOSVoice = optionalString(lookup, "FAIRY_MACOS_VOICE", config.MacOSVoice)
	config.ComfyUIEndpoint = optionalString(lookup, "FAIRY_COMFYUI_ENDPOINT", config.ComfyUIEndpoint)
	config.VolcengineEndpoint = optionalString(lookup, "FAIRY_VOLCENGINE_TTS_ENDPOINT", config.VolcengineEndpoint)
	config.VolcengineAPIKey = optionalSecret(lookup, "FAIRY_VOLCENGINE_TTS_API_KEY", config.VolcengineAPIKey)
	config.VolcengineResourceID = optionalString(lookup, "FAIRY_VOLCENGINE_TTS_RESOURCE_ID", config.VolcengineResourceID)
	config.VolcengineSpeaker = optionalString(lookup, "FAIRY_VOLCENGINE_TTS_SPEAKER", config.VolcengineSpeaker)
	config.VolcengineFormat = optionalString(lookup, "FAIRY_VOLCENGINE_TTS_FORMAT", config.VolcengineFormat)
	config.VolcengineUserID = optionalString(lookup, "FAIRY_VOLCENGINE_TTS_USER_ID", config.VolcengineUserID)
	config.VolcengineSampleRate = volcengineSampleRate
	config.PluginManifestPath = optionalString(lookup, "FAIRY_PLUGIN_MANIFEST", config.PluginManifestPath)
	config.PluginDir = optionalString(lookup, "FAIRY_PLUGIN_DIR", config.PluginDir)
	return config, nil
}

func optionalString(lookup func(string) (string, bool), key string, fallback string) string {
	value, ok := lookup(key)
	if !ok {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func optionalSecret(lookup func(string) (string, bool), key string, fallback string) string {
	value, ok := lookup(key)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func optionalPositiveInt(lookup func(string) (string, bool), key string, fallback int) (int, error) {
	raw, ok := lookup(key)
	if !ok {
		return fallback, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s 必须大于 0", key)
	}
	return value, nil
}

func parseAgentProvider(value string) (agent.Provider, error) {
	provider := agent.Provider(strings.TrimSpace(value))
	switch provider {
	case agent.ProviderMock, agent.ProviderCodex, agent.ProviderFairy:
		return provider, nil
	default:
		return "", fmt.Errorf("FAIRY_AGENT_ENGINE 不支持 provider %q", value)
	}
}

func parseVoiceProvider(value string) (voice.Provider, error) {
	provider := voice.Provider(strings.TrimSpace(value))
	switch provider {
	case voice.ProviderMock, voice.ProviderMacOS, voice.ProviderVolcengine:
		return provider, nil
	case "gpt-sovits", "gptsovits":
		return "", fmt.Errorf("FAIRY_VOICE_ENGINE 不再支持直连模型 provider %q，请改用 voice-service", value)
	default:
		if isProviderID(string(provider)) {
			return provider, nil
		}
		return "", fmt.Errorf("FAIRY_VOICE_ENGINE 不是有效 provider ID: %q", value)
	}
}

func parseImageProvider(value string) (image.Provider, error) {
	provider := image.Provider(strings.TrimSpace(value))
	switch provider {
	case image.ProviderMock, image.ProviderComfyUI:
		return provider, nil
	default:
		return "", fmt.Errorf("FAIRY_IMAGE_ENGINE 不支持 provider %q", value)
	}
}

func parseSceneProvider(value string) (scene.Provider, error) {
	provider := scene.Provider(strings.TrimSpace(value))
	switch provider {
	case scene.ProviderMock, scene.ProviderCodex:
		return provider, nil
	default:
		return "", fmt.Errorf("FAIRY_SCENE_ENGINE 不支持 provider %q", value)
	}
}

func isProviderID(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if char >= '0' && char <= '9' {
			continue
		}
		if char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}
