package companion

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fairy/interaction"
	"fairy/model"
)

// ParticipationEngine owns public ambient reply|wait|silent decisions.
type ParticipationEngine struct {
	host *CompanionService
}

func (e *ParticipationEngine) DecideParticipation(ctx context.Context, request ParticipationRequest) (ParticipationResult, error) {
	s := e.host
	if ctx == nil {
		return ParticipationResult{}, errors.New("context is required")
	}
	if err := ValidateParticipationRequest(request); err != nil {
		return ParticipationResult{}, err
	}
	if s == nil || s.memoryPort() == nil || s.modelPort() == nil || s.characterCatalog() == nil || s.configSource() == nil {
		return ParticipationResult{}, ErrRespondRuntimeNotMigrated
	}
	resolved, err := s.ResolveInteraction(request.ConversationID)
	if err != nil {
		return ParticipationResult{}, err
	}
	if resolved.Memory != interaction.MemoryPublic || !resolved.AllowsAmbientParticipation() {
		return ParticipationResult{}, errors.New("participation requires a public ambient interaction")
	}
	bootstrap, err := s.memoryPort().LoadConversation(request.ConversationID)
	if err != nil {
		return ParticipationResult{}, fmt.Errorf("loading ambient conversation: %w", err)
	}
	record, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return ParticipationResult{}, err
	}
	presence, err := DeriveRecentPresence(bootstrap.Messages, time.Now().UnixMilli())
	if err != nil {
		return ParticipationResult{}, err
	}
	input, err := BuildParticipationInputWithSignals(record, resolved, request.EvaluationReason, request.Messages, presence, time.Now().UnixMilli(), bootstrap.Messages)
	if err != nil {
		return ParticipationResult{}, err
	}
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		return ParticipationResult{}, err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneParticipate)
	}
	events, err := s.modelPort().ExecuteRequestContext(ctx, model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneParticipate, Model: connection.Model,
			Instructions: ParticipationInstructions, MaxOutputTokens: ParticipationMaxOutputTokens,
			PromptCacheKey: cacheKey,
		},
		Input: input,
	})
	if err != nil {
		return ParticipationResult{}, fmt.Errorf("executing participation decision: %w", err)
	}
	if len(model.FunctionCallsFromEvents(events)) != 0 {
		return ParticipationResult{}, errors.New("participation decision returned tool calls")
	}
	return CompileParticipation(model.CollectTextFromEvents(events), request.Messages)
}
