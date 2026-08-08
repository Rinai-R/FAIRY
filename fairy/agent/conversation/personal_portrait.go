package conversation

import (
	"context"
	"unicode/utf8"

	"fairy/context/recall"
	"fairy/transport/session"
)

func (s *Service) retrieveCompanionPortrait(ctx context.Context, characterID, query string, resolved session.Resolved) (recall.Context, error) {
	if !resolved.AllowsPersonalMemory() {
		return recall.Context{}, nil
	}
	private, err := s.memory.turn.portrait.CompanionPortraitContext(ctx, characterID)
	if err != nil {
		return recall.Context{}, err
	}
	if utf8.RuneCountInString(query) > 2000 {
		query = string([]rune(query)[:2000])
	}
	social, err := s.memory.ambient.socialRetrieval.RetrieveCharacterSocialMemoryContext(ctx, characterID, query)
	if err != nil {
		return recall.Context{}, err
	}
	return recall.Context{PersonalMemories: private.PersonalMemories, SemanticStatus: private.SemanticStatus, SocialMemories: social}, nil
}
