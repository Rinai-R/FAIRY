package runtime

import (
	"errors"

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
	{Name: "companion", Owns: "conversation turns, participation, prompt compilation", DependsOn: []string{"memory", "model"}},
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

func validateInjectedDependencies(deps *Dependencies) error {
	if deps == nil {
		return nil
	}
	if deps.MemoryStore == nil {
		return errors.New("injected memory store is required")
	}
	if deps.SecretStore == nil {
		return errors.New("injected secret store is required")
	}
	return nil
}

var _ companion.ModelPort = (*model.ModelService)(nil)
