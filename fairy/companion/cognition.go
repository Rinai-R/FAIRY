package companion

import (
	"context"
	"errors"
	"time"

	"fairy/memory"
	"fairy/model"
	"fairy/search"
	"fairy/tooling"

	domain "fairy/interaction"
)

const (
	toolMemorySearch           = tooling.MemorySearch
	toolPublicMemorySearch     = tooling.PublicMemorySearch
	toolWebSearch              = tooling.WebSearch
	toolSocialExpressionSelect = tooling.SocialExpressionSelect
	toolSocialContextSearch    = tooling.SocialContextSearch
	maxProtocolCompileRetries  = 2
	maxExpressionSelectResults = 5
	maxSocialContextResults    = 5
	runtimeLedgerEventGather   = "gather"
	runtimeLedgerEventTool     = "tool"
)

// RespondInstructionsAllowTools extends reply rules with native function tools.
const RespondInstructionsAllowTools = tooling.RespondInstructionsAllowTools

const RespondInstructionsAllowPublicTools = tooling.RespondInstructionsAllowPublicTools

func RespondToolSpecs(webSearchEnabled bool) []model.ToolSpec {
	return tooling.ToolSpecs(webSearchEnabled)
}

func RespondToolSpecsForInteraction(webSearchEnabled bool, resolved domain.Resolved) []model.ToolSpec {
	return tooling.ToolSpecsForInteraction(webSearchEnabled, resolved)
}

func respondToolSpecsForRuntime(webSearchEnabled bool, resolved domain.Resolved, desktopEnabled bool) []model.ToolSpec {
	tools := tooling.ToolSpecsForInteraction(webSearchEnabled, resolved)
	if desktopEnabled {
		tools = append(tools, desktopToolSpec())
	}
	return tools
}

func RespondInstructionsForTools(toolsEnabled bool) string {
	return tooling.InstructionsForTools(toolsEnabled)
}

func modelDrivenToolBudget(resolved domain.Resolved) int {
	return tooling.ModelDrivenToolBudget(resolved)
}

func RespondInstructionsForInteraction(toolsEnabled bool, resolved domain.Resolved) string {
	return tooling.InstructionsForInteraction(toolsEnabled, resolved)
}

func parseToolQuery(arguments string) (string, error) {
	return tooling.ParseQuery(arguments)
}

func (s *CompanionService) retrieveMemoryForTool(characterID string, query string) (memory.RetrievalContext, error) {
	if s == nil || s.memory.turn.memoryRetrieval == nil {
		return memory.RetrievalContext{}, errors.New("memory store is unavailable")
	}
	var result memory.RetrievalContext
	var err error
	if s.semanticEmbedder != nil && s.semanticEmbedder.Ready() {
		result, err = s.memory.turn.memoryRetrieval.RetrieveWithSemanticVectorIndex(context.Background(), characterID, query, s.semanticEmbedder, s.vectorIndex)
	} else {
		result, err = s.memory.turn.memoryRetrieval.Retrieve(characterID, query)
	}
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
		if len(knowledge) >= limit {
			break
		}
	}
	// Social memory is trigram-only today; do not claim vector semantic fusion.
	return memory.RetrievalContext{Knowledge: knowledge, SemanticStatus: "unavailable"}, nil
}

func mergeRetrievalContext(base memory.RetrievalContext, extra memory.RetrievalContext) memory.RetrievalContext {
	return tooling.MergeRetrievalContext(base, extra)
}

func retrievalFromWebHits(hits []search.Hit) memory.RetrievalContext {
	return tooling.FromWebHits(hits)
}

func retrievalFromToolError(toolName string, err error) memory.RetrievalContext {
	return tooling.FromToolError(toolName, err)
}
