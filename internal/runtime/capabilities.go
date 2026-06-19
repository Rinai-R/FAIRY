package runtime

import (
	"sort"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/image"
	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func (r *Runtime) Capabilities() app.Capabilities {
	capabilities := app.Capabilities{
		Providers: app.ProviderCatalog{
			Agents: agentProviders(r.agents),
			Voices: voiceProviders(r.voices),
			Images: imageProviders(r.images),
			Scenes: sceneProviders(r.scenes),
		},
		Defaults: app.RuntimeConfig{
			AgentProvider: string(r.defaultAgent),
			VoiceProvider: string(r.defaultVoice),
			ImageProvider: string(r.defaultImage),
			SceneProvider: string(r.defaultScene),
		},
		Features: []string{
			"turn",
			"turn_stream",
			"sessions",
			"voice_playback",
			"voice_clone",
			"scene_image",
			"teaching_scene_generation",
			"workflow_advance",
			"frontend_prompt_injection",
			"provider_health",
			"scene_generation",
			"plugin_manifest",
		},
		DesktopReady:   true,
		PluginManifest: "configs/plugins.json",
	}
	r.enrichProviderInfo(&capabilities)
	return capabilities
}

func (r *Runtime) enrichProviderInfo(capabilities *app.Capabilities) {
	if r.plugins == nil {
		return
	}
	catalog := r.plugins.Load()
	for _, manifest := range catalog.Manifests {
		for _, provider := range manifest.Providers {
			applyProviderManifest(capabilities, provider)
		}
	}
}

func applyProviderManifest(capabilities *app.Capabilities, provider app.PluginProvider) {
	lists := map[string]*[]app.ProviderInfo{
		"agent": &capabilities.Providers.Agents,
		"voice": &capabilities.Providers.Voices,
		"image": &capabilities.Providers.Images,
		"scene": &capabilities.Providers.Scenes,
	}
	items, ok := lists[provider.Domain]
	if !ok {
		return
	}
	for index := range *items {
		if (*items)[index].ID != provider.ID {
			continue
		}
		if provider.DisplayName != "" {
			(*items)[index].DisplayName = provider.DisplayName
		}
		if provider.DefaultEndpoint != "" {
			if (*items)[index].Config == nil {
				(*items)[index].Config = map[string]string{}
			}
			(*items)[index].Config["endpoint"] = provider.DefaultEndpoint
		}
		if provider.Adapter != "" {
			if (*items)[index].Config == nil {
				(*items)[index].Config = map[string]string{}
			}
			(*items)[index].Config["adapter"] = provider.Adapter
		}
	}
}

func agentProviders(items map[agent.Provider]agent.Engine) []app.ProviderInfo {
	out := make([]app.ProviderInfo, 0, len(items))
	for provider := range items {
		out = append(out, providerInfo("agent", string(provider)))
	}
	return sortProviders(out)
}

func voiceProviders(items map[voice.Provider]voice.Engine) []app.ProviderInfo {
	out := make([]app.ProviderInfo, 0, len(items))
	for provider := range items {
		info := providerInfo("voice", string(provider))
		out = append(out, info)
	}
	return sortProviders(out)
}

func imageProviders(items map[image.Provider]image.Engine) []app.ProviderInfo {
	out := make([]app.ProviderInfo, 0, len(items))
	for provider := range items {
		out = append(out, providerInfo("image", string(provider)))
	}
	return sortProviders(out)
}

func sceneProviders(items map[scene.Provider]scene.Engine) []app.ProviderInfo {
	out := make([]app.ProviderInfo, 0, len(items))
	for provider := range items {
		out = append(out, providerInfo("scene", string(provider)))
	}
	return sortProviders(out)
}

func providerInfo(domain string, id string) app.ProviderInfo {
	info := app.ProviderInfo{
		ID:          id,
		Domain:      domain,
		DisplayName: id,
		Kind:        "adapter",
		Local:       id == "mock" || id == "macos" || id == "codex" || id == "comfyui",
	}
	switch id {
	case "mock":
		info.DisplayName = "Mock"
	case "codex":
		if domain == "scene" {
			info.DisplayName = "Codex Scene"
		} else {
			info.DisplayName = "Codex CLI"
		}
	case "fairy-agent":
		info.DisplayName = "FAIRY Agent"
		info.Local = false
		info.Config = map[string]string{"endpoint": "http://127.0.0.1:8788"}
	case "macos":
		info.DisplayName = "macOS say"
	case "volcengine":
		info.DisplayName = "火山声音复刻"
		info.Config = map[string]string{"endpoint": "https://openspeech.bytedance.com/api/v3/tts/unidirectional", "resource_id": "seed-icl-2.0", "voice_clone": "true", "format": "mp3"}
	case "comfyui":
		info.DisplayName = "ComfyUI"
		info.Config = map[string]string{"endpoint": "http://127.0.0.1:8188"}
	}
	return info
}

func sortProviders(items []app.ProviderInfo) []app.ProviderInfo {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Domain == items[j].Domain {
			return items[i].ID < items[j].ID
		}
		return items[i].Domain < items[j].Domain
	})
	return items
}
