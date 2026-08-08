package initiative

import (
	"errors"

	"fairy/character"
	"fairy/model"
	"fairy/persona"
	"fairy/session"
)

// buildSocialStablePrefix returns only public, revision-scoped context that is
// safe to place before per-batch observations and feedback evidence.
func buildSocialStablePrefix(record character.Record, resolved session.Resolved) ([]model.PromptItem, error) {
	if resolved.Memory != session.MemoryPublic || !resolved.AllowsAmbientParticipation() {
		return nil, errors.New("social experience requires a public ambient interaction")
	}
	characterItem, err := persona.EncodeCharacterContext(record)
	if err != nil {
		return nil, err
	}
	interactionItem, err := persona.EncodeInteractionContext(resolved)
	if err != nil {
		return nil, err
	}
	return []model.PromptItem{characterItem, interactionItem}, nil
}
