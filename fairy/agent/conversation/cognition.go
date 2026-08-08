package conversation

import (
	"context"
	"errors"
	"time"

	knowledgectx "fairy/context/knowledge"
	"fairy/context/recall"
	"fairy/context/social"
)

const (
	maxProtocolCompileRetries  = 2
	maxExpressionSelectResults = 5
	maxSocialContextResults    = 5
	runtimeLedgerEventGather   = "gather"
	runtimeLedgerEventTool     = "tool"
)

func (s *Service) retrieveMemoryForTool(characterID string, query string) (recall.Context, error) {
	if s == nil || s.memory.turn.memoryRetrieval == nil {
		return recall.Context{}, errors.New("memory store is unavailable")
	}
	private, err := s.memory.turn.memoryRetrieval.Retrieve(characterID, query)
	if err != nil {
		return recall.Context{}, err
	}
	social, err := s.memory.ambient.socialRetrieval.RetrieveCharacterSocialMemoryContext(context.Background(), characterID, query)
	if err != nil {
		return recall.Context{}, err
	}
	result := recall.Context{PersonalMemories: private.PersonalMemories, SemanticStatus: private.SemanticStatus, SocialMemories: social}
	if s.memory.turn.knowledge != nil {
		public, err := s.memory.turn.knowledge.RetrieveContext(context.Background(), query)
		if err != nil {
			return recall.Context{}, err
		}
		result.Knowledge = public.Entries
		if public.SemanticStatus != "" {
			result.SemanticStatus = public.SemanticStatus
		}
	}
	return result, nil
}

func (s *Service) retrievePublicKnowledgeForTool(ctx context.Context, query string) (recall.Context, error) {
	if s == nil || s.memory.turn.knowledge == nil {
		return recall.Context{}, errors.New("knowledge store is unavailable")
	}
	retrieval, err := s.memory.turn.knowledge.RetrieveContext(ctx, query)
	if err != nil {
		return recall.Context{}, err
	}
	return recall.Context{Knowledge: retrieval.Entries, SemanticStatus: retrieval.SemanticStatus}, nil
}

func (s *Service) selectSocialExpressionsForTool(ctx context.Context, characterID, conversationID, query string) (recall.Context, error) {
	return s.selectSocialMemoryKindsForTool(ctx, characterID, conversationID, query, []string{social.SocialMemoryExpression}, "social_expression", maxExpressionSelectResults)
}

func (s *Service) selectSocialContextForTool(ctx context.Context, characterID, conversationID, query string) (recall.Context, error) {
	return s.selectSocialMemoryKindsForTool(ctx, characterID, conversationID, query, []string{social.SocialMemoryEpisode, social.SocialMemoryBehavior}, "social_context", maxSocialContextResults)
}

func (s *Service) selectSocialMemoryKindsForTool(
	ctx context.Context,
	characterID, conversationID, query string,
	kinds []string,
	layerPrefix string,
	limit int,
) (recall.Context, error) {
	if s == nil || s.memory.ambient.socialRetrieval == nil {
		return recall.Context{}, errors.New("memory store is unavailable")
	}
	retrieved, err := s.memory.ambient.socialRetrieval.RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
	if err != nil {
		return recall.Context{}, err
	}
	allowed := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = struct{}{}
	}
	now := time.Now().UnixMilli()
	knowledge := make([]knowledgectx.Retrieved, 0, limit)
	selected := make([]social.SocialMemoryEntry, 0, limit)
	for _, entry := range retrieved.Entries {
		if _, ok := allowed[entry.Kind]; !ok {
			continue
		}
		layer := "social_" + entry.Kind
		if layerPrefix == "social_expression" {
			layer = "social_expression"
		}
		knowledge = append(knowledge, knowledgectx.Retrieved{
			ID:                    entry.ID,
			Layer:                 layer,
			Topic:                 entry.Situation,
			Statement:             entry.Content,
			VerificationBasis:     layerPrefix,
			ConfidenceBasisPoints: 6000,
			UpdatedAtUnixMS:       now,
		})
		selected = append(selected, entry)
		if len(knowledge) >= limit {
			break
		}
	}
	// Social memory is trigram-only today; do not claim vector semantic fusion.
	return recall.Context{
		Knowledge: knowledge, SocialMemories: social.SocialMemoryContext{Entries: selected},
		SemanticStatus: "unavailable",
	}, nil
}
