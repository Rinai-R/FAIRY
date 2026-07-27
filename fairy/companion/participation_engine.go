package companion

import (
	"context"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/participation"

	domain "fairy/interaction"
)

type (
	AmbientObservation            = participation.AmbientObservation
	ParticipationEvaluationReason = participation.ParticipationEvaluationReason
	ParticipationRequest          = participation.ParticipationRequest
	ParticipationAction           = participation.ParticipationAction
	ReplyIntent                   = participation.ReplyIntent
	RecentPresence                = participation.RecentPresence
	ParticipationSignals          = participation.ParticipationSignals
	ParticipationResult           = participation.ParticipationResult
)

const (
	ParticipationInstructions      = participation.ParticipationInstructions
	ParticipationMaxOutputTokens   = participation.ParticipationMaxOutputTokens
	ParticipationReasonMessage     = participation.ParticipationReasonMessage
	ParticipationReasonWaitElapsed = participation.ParticipationReasonWaitElapsed
	ParticipationReply             = participation.ParticipationReply
	ParticipationWait              = participation.ParticipationWait
	ParticipationSilent            = participation.ParticipationSilent
)

type ParticipationEngine = participation.Engine

func newParticipationEngine(service *CompanionService) *ParticipationEngine {
	return participation.NewEngine(participationHost{service: service})
}

type participationHost struct {
	service *CompanionService
}

var _ participation.DecisionHost = participationHost{}

func (h participationHost) LoadConversationActivity(conversationID string, nowUnixMS int64) (memory.ConversationActivity, error) {
	if h.service == nil || h.service.memory.ambient.activity == nil {
		return memory.ConversationActivity{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.memory.ambient.activity.LoadConversationActivity(conversationID, nowUnixMS)
}

func (h participationHost) ResolveInteraction(conversationID string) (domain.Resolved, error) {
	if h.service == nil {
		return domain.Resolved{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.ResolveInteraction(conversationID)
}

func (h participationHost) ActiveCharacter(characterID string) (character.Record, error) {
	if h.service == nil || h.service.characterLookup == nil {
		return character.Record{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.activeCharacter(characterID)
}

func (h participationHost) ListSocialPersonNotes(ctx context.Context, characterID, conversationID string, senderIDs []string) ([]memory.SocialPersonNote, error) {
	if h.service == nil || h.service.memory.ambient.socialContext == nil {
		return nil, ErrRespondRuntimeNotMigrated
	}
	return h.service.memory.ambient.socialContext.ListSocialPersonNotes(ctx, characterID, conversationID, senderIDs)
}

func (h participationHost) RetrieveSocialMemoryContext(ctx context.Context, characterID, conversationID, query string) (memory.SocialMemoryContext, error) {
	if h.service == nil || h.service.memory.ambient.socialRetrieval == nil {
		return memory.SocialMemoryContext{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.memory.ambient.socialRetrieval.RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
}

func (h participationHost) ModelConnection() (config.ModelConnection, error) {
	if h.service == nil || h.service.configSource() == nil {
		return config.ModelConnection{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.configSource().ModelConnection()
}

func (h participationHost) ExecuteRequest(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	if h.service == nil || h.service.modelPort() == nil {
		return nil, ErrRespondRuntimeNotMigrated
	}
	return h.service.modelPort().ExecuteRequestContext(ctx, request)
}

func (s *CompanionService) participationBehaviorContext(ctx context.Context, characterID, conversationID string, messages []AmbientObservation) (*model.PromptItem, error) {
	if s == nil || s.memory.ambient.socialRetrieval == nil {
		return nil, nil
	}
	query := participation.BehaviorQuery(messages)
	if query == "" {
		query = "群聊互动"
	}
	retrieved, err := s.memory.ambient.socialRetrieval.RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
	if err != nil {
		return nil, err
	}
	return participation.BehaviorItem(retrieved)
}

var participationBehaviorQuery = participation.BehaviorQuery
