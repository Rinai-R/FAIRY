package presence

import (
	"errors"

	"fairy/context/character"
	"fairy/runtime/model"
	"fairy/transport/session"
)

// buildSocialStablePrefix returns only public, revision-scoped context that is
// safe to place before per-batch observations and feedback evidence.
func buildSocialStablePrefix(record character.Record, resolved session.Resolved) ([]model.PromptItem, error) {
	if resolved.Memory != session.MemoryPublic || !resolved.AllowsAmbientParticipation() {
		return nil, errors.New("social experience requires a public ambient interaction")
	}
	characterItem, err := character.EncodeCharacterContext(record)
	if err != nil {
		return nil, err
	}
	interactionItem, err := character.EncodeInteractionContext(resolved)
	if err != nil {
		return nil, err
	}
	return []model.PromptItem{characterItem, interactionItem}, nil
}
