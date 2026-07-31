package companion

import (
	"context"
	"errors"
	"time"

	"fairy/memory"
	"fairy/model"

	"fairy/session"
)

const (
	maxProtocolCompileRetries  = 2
	maxExpressionSelectResults = 5
	maxSocialContextResults    = 5
	runtimeLedgerEventGather   = "gather"
	runtimeLedgerEventTool     = "tool"
)

func respondToolSpecsForRuntime(webSearchEnabled bool, resolved session.Resolved, desktopEnabled, stickerEnabled bool) []model.ToolSpec {
	tools := respondToolSpecsForInteraction(webSearchEnabled, resolved)
	if desktopEnabled {
		tools = append(tools, desktopToolSpec())
	}
	if stickerEnabled {
		tools = append(tools, stickerToolSpec())
	}
	return tools
}

func (s *CompanionService) retrieveMemoryForTool(characterID string, query string) (memory.RetrievalContext, error) {
	if s == nil || s.memory.turn.memoryRetrieval == nil {
		return memory.RetrievalContext{}, errors.New("memory store is unavailable")
	}
	result, err := s.memory.turn.memoryRetrieval.Retrieve(characterID, query)
	if err != nil {
		return memory.RetrievalContext{}, err
	}
	social, err := s.memory.turn.memoryRetrieval.RetrieveCharacterSocialMemoryContext(context.Background(), characterID, query)
	if err != nil {
		return memory.RetrievalContext{}, err
	}
	result.SocialMemories = social
	return result, nil
}

func (s *CompanionService) retrievePublicKnowledgeForTool(ctx context.Context, query string) (memory.RetrievalContext, error) {
	if s == nil || s.memory.turn.memoryRetrieval == nil {
		return memory.RetrievalContext{}, errors.New("memory store is unavailable")
	}
	return s.memory.turn.memoryRetrieval.RetrievePublicKnowledgeContext(ctx, query)
}

func (s *CompanionService) selectSocialExpressionsForTool(ctx context.Context, characterID, conversationID, query string) (memory.RetrievalContext, error) {
	return s.selectSocialMemoryKindsForTool(ctx, characterID, conversationID, query, []string{memory.SocialMemoryExpression}, "social_expression", maxExpressionSelectResults)
}

func (s *CompanionService) selectSocialContextForTool(ctx context.Context, characterID, conversationID, query string) (memory.RetrievalContext, error) {
	return s.selectSocialMemoryKindsForTool(ctx, characterID, conversationID, query, []string{memory.SocialMemoryEpisode, memory.SocialMemoryBehavior}, "social_context", maxSocialContextResults)
}

func (s *CompanionService) selectSocialMemoryKindsForTool(
	ctx context.Context,
	characterID, conversationID, query string,
	kinds []string,
	layerPrefix string,
	limit int,
) (memory.RetrievalContext, error) {
	if s == nil || s.memory.ambient.socialRetrieval == nil {
		return memory.RetrievalContext{}, errors.New("memory store is unavailable")
	}
	retrieved, err := s.memory.ambient.socialRetrieval.RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
	if err != nil {
		return memory.RetrievalContext{}, err
	}
	allowed := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = struct{}{}
	}
	now := time.Now().UnixMilli()
	knowledge := make([]memory.RetrievedKnowledge, 0, limit)
	selected := make([]memory.SocialMemoryEntry, 0, limit)
	for _, entry := range retrieved.Entries {
		if _, ok := allowed[entry.Kind]; !ok {
			continue
		}
		layer := "social_" + entry.Kind
		if layerPrefix == "social_expression" {
			layer = "social_expression"
		}
		knowledge = append(knowledge, memory.RetrievedKnowledge{
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
	return memory.RetrievalContext{
		Knowledge: knowledge, SocialMemories: memory.SocialMemoryContext{Entries: selected},
		SemanticStatus: "unavailable",
	}, nil
}
