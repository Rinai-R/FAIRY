package companion

import (
	"context"
	"unicode/utf8"

	"fairy/memory"

	"fairy/session"
)

func (s *CompanionService) retrieveCompanionPortrait(ctx context.Context, characterID, query string, resolved session.Resolved) (memory.RetrievalContext, error) {
	if !resolved.AllowsPersonalMemory() {
		return memory.RetrievalContext{}, nil
	}
	portrait, err := s.memory.turn.portrait.CompanionPortraitContext(ctx, characterID)
	if err != nil {
		return memory.RetrievalContext{}, err
	}
	if utf8.RuneCountInString(query) > 2000 {
		query = string([]rune(query)[:2000])
	}
	social, err := s.memory.turn.portrait.RetrieveCharacterSocialMemoryContext(ctx, characterID, query)
	if err != nil {
		return memory.RetrievalContext{}, err
	}
	portrait.SocialMemories = social
	return portrait, nil
}
