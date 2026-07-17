package notify

import "encoding/json"

// ConfigurationChange mirrors the Tauri companion-configuration-changed payload.
// Frontend parsers require exact key sets per category.
type ConfigurationChange struct {
	Category   string
	Revision   *uint64
	Configured *bool
	Ready      *bool
}

func CharacterChanged(revision uint64) ConfigurationChange {
	value := revision
	return ConfigurationChange{Category: "character", Revision: &value}
}

func UserProfileChanged(revision *uint64) ConfigurationChange {
	return ConfigurationChange{Category: "user_profile", Revision: revision}
}

func ModelChanged(configured bool, ready bool) ConfigurationChange {
	configuredValue := configured
	readyValue := ready
	return ConfigurationChange{
		Category:   "model",
		Configured: &configuredValue,
		Ready:      &readyValue,
	}
}

func (c ConfigurationChange) MarshalJSON() ([]byte, error) {
	switch c.Category {
	case "character":
		revision := uint64(0)
		if c.Revision != nil {
			revision = *c.Revision
		}
		return json.Marshal(struct {
			Category string `json:"category"`
			Revision uint64 `json:"revision"`
		}{Category: "character", Revision: revision})
	case "user_profile":
		return json.Marshal(struct {
			Category string  `json:"category"`
			Revision *uint64 `json:"revision"`
		}{Category: "user_profile", Revision: c.Revision})
	case "model":
		configured := false
		ready := false
		if c.Configured != nil {
			configured = *c.Configured
		}
		if c.Ready != nil {
			ready = *c.Ready
		}
		return json.Marshal(struct {
			Category   string `json:"category"`
			Configured bool   `json:"configured"`
			Ready      bool   `json:"ready"`
		}{Category: "model", Configured: configured, Ready: ready})
	default:
		return json.Marshal(struct {
			Category string `json:"category"`
		}{Category: c.Category})
	}
}

// ConfigEmitter delivers configuration-change events. Callers (main) inject an
// implementation into character/config/profile services — there is no package
// global emitter.
type ConfigEmitter func(ConfigurationChange)
