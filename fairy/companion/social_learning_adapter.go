package companion

import (
	"context"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/sociallearning"

	"go.uber.org/zap"

	domain "fairy/interaction"
)

type socialLearningHost struct {
	service *CompanionService
}

var (
	_ sociallearning.LearningHost = socialLearningHost{}
	_ sociallearning.FeedbackHost = socialLearningHost{}
)

func (h socialLearningHost) ResolveInteraction(conversationID string) (domain.Resolved, error) {
	if h.service == nil {
		return domain.Resolved{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.ResolveInteraction(conversationID)
}

func (h socialLearningHost) LoadConversation(conversationID string) (memory.ConversationBootstrap, error) {
	if h.service == nil || h.service.memoryPort() == nil {
		return memory.ConversationBootstrap{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.memoryPort().LoadConversation(conversationID)
}

func (h socialLearningHost) ActiveCharacter(characterID string) (character.Record, error) {
	if h.service == nil || h.service.characterCatalog() == nil {
		return character.Record{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.activeCharacter(characterID)
}

func (h socialLearningHost) ModelConnection() (config.ModelConnection, error) {
	if h.service == nil || h.service.configSource() == nil {
		return config.ModelConnection{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.configSource().ModelConnection()
}

func (h socialLearningHost) ExecuteRequest(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	if h.service == nil || h.service.modelPort() == nil {
		return nil, ErrRespondRuntimeNotMigrated
	}
	return h.service.modelPort().ExecuteRequestContext(ctx, request)
}

func (h socialLearningHost) StoreSocialMemoryEntries(ctx context.Context, input memory.SocialMemoryBatchInput) ([]memory.SocialMemoryEntry, error) {
	if h.service == nil || h.service.memoryPort() == nil {
		return nil, ErrRespondRuntimeNotMigrated
	}
	return h.service.memoryPort().StoreSocialMemoryEntries(ctx, input)
}

func (h socialLearningHost) UpsertSocialPersonNote(ctx context.Context, input memory.SocialPersonNoteInput) (memory.SocialPersonNote, error) {
	if h.service == nil || h.service.memoryPort() == nil {
		return memory.SocialPersonNote{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.memoryPort().UpsertSocialPersonNote(ctx, input)
}

func (h socialLearningHost) RecordSocialReplyFeedback(ctx context.Context, input memory.SocialReplyFeedbackInput) (memory.SocialReplyFeedback, error) {
	if h.service == nil || h.service.memoryPort() == nil {
		return memory.SocialReplyFeedback{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.memoryPort().RecordSocialReplyFeedback(ctx, input)
}

func (h socialLearningHost) WarnLearning(conversationID string, err error) {
	h.warn("social learning failed", conversationID, "", err)
}

func (h socialLearningHost) WarnFeedback(conversationID, turnID string, err error) {
	h.warn("social feedback failed", conversationID, turnID, err)
}

func (h socialLearningHost) warn(message, conversationID, turnID string, err error) {
	if h.service == nil || h.service.logger == nil {
		return
	}
	fields := []zap.Field{zap.String("conversationId", conversationID), zap.Error(err)}
	if turnID != "" {
		fields = append(fields, zap.String("turnId", turnID))
	}
	h.service.logger.Warn(message, fields...)
}
