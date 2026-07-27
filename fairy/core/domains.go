package core

import (
	"fairy/companion"
	"fairy/model"
)

// Domain describes a Core-owned domain and its public wiring boundary.
// It is intentionally metadata-only: business packages remain independent
// and runtime remains the composition root.
type Domain struct {
	Name        string
	Owns        string
	DependsOn   []string
	Composition bool
}

var coreDomains = []Domain{
	{Name: "companion", Owns: "conversation turn lifecycle, orchestration and background personal-memory jobs", DependsOn: []string{"persona", "participation", "reply", "sociallearning", "memory", "model"}},
	{Name: "persona", Owns: "prompt instructions, context slots and stable-prefix compilation", DependsOn: []string{"memory", "model", "reply"}},
	{Name: "reply", Owns: "compiled replies, pacing, delivery and speech pipeline"},
	{Name: "participation", Owns: "ambient participation decisions, inbox state and participation events", DependsOn: []string{"persona", "memory", "model"}},
	{Name: "sociallearning", Owns: "public observation learning and reply feedback attribution", DependsOn: []string{"persona", "participation", "memory", "model"}},
	{Name: "observation", Owns: "desktop observation decisions and trigger routing"},
	{Name: "interaction", Owns: "interaction policy resolution from durable facts"},
	{Name: "memory", Owns: "conversation, profile evidence, knowledge and feedback persistence"},
	{Name: "model", Owns: "provider request compilation and transport"},
	{Name: "runtime", Owns: "lifecycle and cross-domain construction", Composition: true},
}

// Domains returns a copy so callers cannot mutate the runtime's boundary map.
func Domains() []Domain {
	result := make([]Domain, len(coreDomains))
	copy(result, coreDomains)
	for i := range result {
		result[i].DependsOn = append([]string(nil), result[i].DependsOn...)
	}
	return result
}

var _ companion.ModelPort = (*model.ModelService)(nil)
