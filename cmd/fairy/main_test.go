package main

import (
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/image"
	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
)

func TestConfigFromLookupUsesDefaults(t *testing.T) {
	config, err := configFromLookup(mapLookup(nil))
	if err != nil {
		t.Fatalf("configFromLookup() error = %v", err)
	}

	defaults := bootstrap.DefaultConfig()
	if config.AgentProvider != defaults.AgentProvider {
		t.Fatalf("AgentProvider = %q, want %q", config.AgentProvider, defaults.AgentProvider)
	}
	if config.VoiceProvider != defaults.VoiceProvider {
		t.Fatalf("VoiceProvider = %q, want %q", config.VoiceProvider, defaults.VoiceProvider)
	}
	if config.CodexTimeout != defaults.CodexTimeout {
		t.Fatalf("CodexTimeout = %d, want %d", config.CodexTimeout, defaults.CodexTimeout)
	}
}

func TestConfigFromLookupAppliesValidOverrides(t *testing.T) {
	config, err := configFromLookup(mapLookup(map[string]string{
		"FAIRY_AGENT_ENGINE":               "codex",
		"FAIRY_VOICE_ENGINE":               "volcengine",
		"FAIRY_IMAGE_ENGINE":               "comfyui",
		"FAIRY_SCENE_ENGINE":               "codex",
		"FAIRY_CODEX_TIMEOUT_SECONDS":      "30",
		"FAIRY_VOLCENGINE_TTS_SAMPLE_RATE": "24000",
		"FAIRY_VOLCENGINE_TTS_API_KEY":     "secret with spaces inside",
		"FAIRY_VOLCENGINE_TTS_RESOURCE_ID": "volc.tts",
		"FAIRY_COMFYUI_ENDPOINT":           "http://127.0.0.1:8188",
		"FAIRY_PLUGIN_MANIFEST":            "configs/plugins.json",
		"FAIRY_PLUGIN_DIR":                 "plugins",
		"FAIRY_CODEX_MODEL":                "gpt-5-codex",
		"FAIRY_CODEX_WORKDIR":              "/tmp/fairy",
		"FAIRY_CODEX_SESSION_PATH":         "data/codex-session.json",
		"FAIRY_SESSION_PATH":               "data/sessions.json",
		"FAIRY_AUDIO_DIR":                  "data/audio",
		"FAIRY_AUDIO_BASE_URL":             "/audio",
		"FAIRY_IMAGE_DIR":                  "data/images",
		"FAIRY_MATERIAL_DIR":               "data/materials",
		"FAIRY_IMAGE_BASE_URL":             "/images",
		"FAIRY_VOLCENGINE_TTS_ENDPOINT":    "https://openspeech.bytedance.com/api/v3/tts/unidirectional",
		"FAIRY_VOLCENGINE_TTS_SPEAKER":     "zh_female",
		"FAIRY_VOLCENGINE_TTS_FORMAT":      "mp3",
		"FAIRY_VOLCENGINE_TTS_USER_ID":     "fairy-local",
	}))
	if err != nil {
		t.Fatalf("configFromLookup() error = %v", err)
	}

	if config.AgentProvider != agent.ProviderCodex {
		t.Fatalf("AgentProvider = %q, want %q", config.AgentProvider, agent.ProviderCodex)
	}
	if config.VoiceProvider != voice.ProviderVolcengine {
		t.Fatalf("VoiceProvider = %q, want %q", config.VoiceProvider, voice.ProviderVolcengine)
	}
	if config.ImageProvider != image.ProviderComfyUI {
		t.Fatalf("ImageProvider = %q, want %q", config.ImageProvider, image.ProviderComfyUI)
	}
	if config.SceneProvider != scene.ProviderCodex {
		t.Fatalf("SceneProvider = %q, want %q", config.SceneProvider, scene.ProviderCodex)
	}
	if config.CodexTimeout != 30 {
		t.Fatalf("CodexTimeout = %d, want 30", config.CodexTimeout)
	}
	if config.VolcengineSampleRate != 24000 {
		t.Fatalf("VolcengineSampleRate = %d, want 24000", config.VolcengineSampleRate)
	}
	if config.VolcengineAPIKey != "secret with spaces inside" {
		t.Fatalf("VolcengineAPIKey was not preserved")
	}
	if config.MaterialDir != "data/materials" {
		t.Fatalf("MaterialDir = %q, want data/materials", config.MaterialDir)
	}
}

func TestConfigFromLookupAppliesFairyAgentOverrides(t *testing.T) {
	config, err := configFromLookup(mapLookup(map[string]string{
		"FAIRY_AGENT_ENGINE":          "fairy-agent",
		"FAIRY_AGENT_ENDPOINT":        "https://ark.cn-beijing.volces.com/api/v3",
		"FAIRY_AGENT_API_KEY":         "secret with spaces inside",
		"FAIRY_AGENT_MODEL":           "deepseek-v3",
		"FAIRY_AGENT_EXTRA_BODY":      `{"thinking":{"type":"disabled"},"enable_thinking":false}`,
		"FAIRY_AGENT_TIMEOUT_SECONDS": "45",
	}))
	if err != nil {
		t.Fatalf("configFromLookup() error = %v", err)
	}
	if config.AgentProvider != agent.ProviderFairy {
		t.Fatalf("AgentProvider = %q, want %q", config.AgentProvider, agent.ProviderFairy)
	}
	if config.FairyAgentEndpoint != "https://ark.cn-beijing.volces.com/api/v3" {
		t.Fatalf("FairyAgentEndpoint = %q", config.FairyAgentEndpoint)
	}
	if config.FairyAgentAPIKey != "secret with spaces inside" {
		t.Fatalf("FairyAgentAPIKey was not preserved")
	}
	if config.FairyAgentModel != "deepseek-v3" {
		t.Fatalf("FairyAgentModel = %q", config.FairyAgentModel)
	}
	if config.FairyAgentExtraBody != `{"thinking":{"type":"disabled"},"enable_thinking":false}` {
		t.Fatalf("FairyAgentExtraBody = %q", config.FairyAgentExtraBody)
	}
	if config.FairyAgentTimeout != 45 {
		t.Fatalf("FairyAgentTimeout = %d, want 45", config.FairyAgentTimeout)
	}
}

func TestConfigFromLookupRejectsCustomVoiceProviderID(t *testing.T) {
	_, err := configFromLookup(mapLookup(map[string]string{
		"FAIRY_VOICE_ENGINE": "my-voice.plugin_1",
	}))
	if err == nil {
		t.Fatal("configFromLookup() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "FAIRY_VOICE_ENGINE") {
		t.Fatalf("error = %q, want FAIRY_VOICE_ENGINE context", err)
	}
}

func TestConfigFromLookupRejectsInvalidProvider(t *testing.T) {
	_, err := configFromLookup(mapLookup(map[string]string{
		"FAIRY_AGENT_ENGINE": "claude code",
	}))
	if err == nil {
		t.Fatal("configFromLookup() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "FAIRY_AGENT_ENGINE") {
		t.Fatalf("error = %q, want FAIRY_AGENT_ENGINE context", err)
	}
}

func TestConfigFromLookupRejectsUnsupportedVoiceProvider(t *testing.T) {
	for _, provider := range []string{"custom-voice", "local-voice"} {
		t.Run(provider, func(t *testing.T) {
			_, err := configFromLookup(mapLookup(map[string]string{
				"FAIRY_VOICE_ENGINE": provider,
			}))
			if err == nil {
				t.Fatal("configFromLookup() error = nil, want unsupported provider error")
			}
			if !strings.Contains(err.Error(), "FAIRY_VOICE_ENGINE") {
				t.Fatalf("error = %q, want FAIRY_VOICE_ENGINE context", err)
			}
		})
	}
}

func TestConfigFromLookupRejectsInvalidPositiveInt(t *testing.T) {
	tests := map[string]string{
		"FAIRY_CODEX_TIMEOUT_SECONDS":      "abc",
		"FAIRY_AGENT_TIMEOUT_SECONDS":      "-1",
		"FAIRY_VOLCENGINE_TTS_SAMPLE_RATE": "0",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			_, err := configFromLookup(mapLookup(map[string]string{key: value}))
			if err == nil {
				t.Fatal("configFromLookup() error = nil, want error")
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error = %q, want %s context", err, key)
			}
		})
	}
}

func TestConfigFromLookupTreatsBlankOptionalValueAsDefault(t *testing.T) {
	config, err := configFromLookup(mapLookup(map[string]string{
		"FAIRY_CODEX_TIMEOUT_SECONDS": "   ",
		"FAIRY_CODEX_BIN":             "   ",
	}))
	if err != nil {
		t.Fatalf("configFromLookup() error = %v", err)
	}

	defaults := bootstrap.DefaultConfig()
	if config.CodexTimeout != defaults.CodexTimeout {
		t.Fatalf("CodexTimeout = %d, want %d", config.CodexTimeout, defaults.CodexTimeout)
	}
	if config.CodexBin != defaults.CodexBin {
		t.Fatalf("CodexBin = %q, want %q", config.CodexBin, defaults.CodexBin)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
