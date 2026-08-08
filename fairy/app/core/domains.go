package core

import (
	turn "fairy/agent/conversation"
	"fairy/runtime/model"
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
	{Name: "turn", Owns: "reactive turn state, agent loop, delivery, cancellation and runtime ledger", DependsOn: []string{"agenttool", "persona", "reply", "memory", "model"}},
	{Name: "initiative", Owns: "ambient participation, desktop initiative and bounded experience feedback", DependsOn: []string{"persona", "memory", "model"}},
	{Name: "retention", Owns: "bounded post-turn extraction and knowledge-learning lifecycle", DependsOn: []string{"knowledge", "memory", "model", "persona"}},
	{Name: "agenttool", Owns: "agent tool contracts, validation, execution projections and diagnostics", DependsOn: []string{"knowledge", "memory", "model", "sticker", "session"}},
	{Name: "knowledge", Owns: "web retrieval, safe document fetching, cleaning and knowledge action compilation", DependsOn: []string{"memory", "model"}},
	{Name: "persona", Owns: "prompt instructions, context slots and stable-prefix compilation", DependsOn: []string{"memory", "model", "reply"}},
	{Name: "reply", Owns: "compiled replies, pacing and ordered delivery"},
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

var _ turn.ModelPort = (*model.ModelService)(nil)
