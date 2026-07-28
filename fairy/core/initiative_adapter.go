package core

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"fairy/character"
	"fairy/companion"
	"fairy/config"
	"fairy/initiative"
	"fairy/memory"
	"fairy/model"
	"fairy/session"
)

type initiativeAdapter struct {
	companion  *companion.CompanionService
	memory     *memory.Store
	characters *character.CharacterService
	config     *config.Reader
	model      *model.ModelService
	messages   observedMessages
	events     *ParticipationHub
	logger     *zap.Logger
}

type observedMessages interface {
	Begin(source, conversationID string) string
	Participation(traceIDs []string, targetTraceID, action string)
	End(traceID, status string)
}

func (a initiativeAdapter) ResolveInteraction(conversationID string) (session.Resolved, error) {
	if a.companion == nil {
		return session.Resolved{}, companion.ErrTurnRuntimeUnavailable
	}
	return a.companion.ResolveInteraction(conversationID)
}

func (a initiativeAdapter) LoadConversationActivity(conversationID string, nowUnixMS int64) (memory.ConversationActivity, error) {
	if a.memory == nil {
		return memory.ConversationActivity{}, companion.ErrTurnRuntimeUnavailable
	}
	return a.memory.LoadConversationActivity(conversationID, nowUnixMS)
}

func (a initiativeAdapter) LoadConversationRecord(conversationID string) (memory.ConversationRecord, error) {
	if a.memory == nil {
		return memory.ConversationRecord{}, companion.ErrTurnRuntimeUnavailable
	}
	return a.memory.LoadConversationRecord(conversationID)
}

func (a initiativeAdapter) ActiveCharacter(characterID string) (character.Record, error) {
	if a.characters == nil || a.characters.CatalogStore() == nil {
		return character.Record{}, companion.ErrTurnRuntimeUnavailable
	}
	record, found, err := a.characters.CatalogStore().Lookup(characterID)
	if err != nil {
		return character.Record{}, err
	}
	if !found {
		return character.Record{}, errors.New("character not found")
	}
	return record, nil
}

func (a initiativeAdapter) ListSocialPersonNotes(ctx context.Context, characterID, conversationID string, senderIDs []string) ([]memory.SocialPersonNote, error) {
	if a.memory == nil {
		return nil, companion.ErrTurnRuntimeUnavailable
	}
	return a.memory.ListSocialPersonNotes(ctx, characterID, conversationID, senderIDs)
}

func (a initiativeAdapter) RetrieveSocialMemoryContext(ctx context.Context, characterID, conversationID, query string) (memory.SocialMemoryContext, error) {
	if a.memory == nil {
		return memory.SocialMemoryContext{}, companion.ErrTurnRuntimeUnavailable
	}
	return a.memory.RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
}

func (a initiativeAdapter) ModelConnection() (config.ModelConnection, error) {
	if a.config == nil {
		return config.ModelConnection{}, companion.ErrTurnRuntimeUnavailable
	}
	return a.config.ModelConnection()
}

func (a initiativeAdapter) ExecuteRequest(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	if a.model == nil {
		return nil, companion.ErrTurnRuntimeUnavailable
	}
	return a.model.ExecuteRequestContext(ctx, request)
}

func (a initiativeAdapter) StoreSocialMemoryEntries(ctx context.Context, input memory.SocialMemoryBatchInput) ([]memory.SocialMemoryEntry, error) {
	if a.memory == nil {
		return nil, companion.ErrTurnRuntimeUnavailable
	}
	return a.memory.StoreSocialMemoryEntries(ctx, input)
}

func (a initiativeAdapter) UpsertSocialPersonNote(ctx context.Context, input memory.SocialPersonNoteInput) (memory.SocialPersonNote, error) {
	if a.memory == nil {
		return memory.SocialPersonNote{}, companion.ErrTurnRuntimeUnavailable
	}
	return a.memory.UpsertSocialPersonNote(ctx, input)
}

func (a initiativeAdapter) RecordSocialReplyFeedback(ctx context.Context, input memory.SocialReplyFeedbackInput) (memory.SocialReplyFeedback, error) {
	if a.memory == nil {
		return memory.SocialReplyFeedback{}, companion.ErrTurnRuntimeUnavailable
	}
	return a.memory.RecordSocialReplyFeedback(ctx, input)
}

func (a initiativeAdapter) WarnLearning(conversationID string, err error) {
	a.warn("social learning failed", conversationID, "", 0, err)
}

func (a initiativeAdapter) WarnFeedback(conversationID, turnID string, err error) {
	a.warn("social feedback failed", conversationID, turnID, 0, err)
}

func (a initiativeAdapter) CancelTurnBeforeDelivery(conversationID string) {
	if a.companion != nil {
		a.companion.CancelTurnBeforeDelivery(conversationID)
	}
}

func (a initiativeAdapter) SubmitTurn(request initiative.TurnRequest) (initiative.TurnOutcome, error) {
	if a.companion == nil {
		return initiative.TurnOutcome{}, companion.ErrTurnRuntimeUnavailable
	}
	var intent *companion.ReplyIntent
	if request.ReplyIntent != nil {
		intent = &companion.ReplyIntent{
			ReplyAct: request.ReplyIntent.ReplyAct, Tone: request.ReplyIntent.Tone,
			RelationshipSignal: request.ReplyIntent.RelationshipSignal, ReplyMode: request.ReplyIntent.ReplyMode,
			Focus: request.ReplyIntent.Focus, Avoid: append([]string(nil), request.ReplyIntent.Avoid...),
			ReferenceInfo: request.ReplyIntent.ReferenceInfo, MemoryQuery: request.ReplyIntent.MemoryQuery,
			ExpressionQuery: request.ReplyIntent.ExpressionQuery, DriftLevel: request.ReplyIntent.DriftLevel,
			AnchorPolicy: request.ReplyIntent.AnchorPolicy,
		}
	}
	outcome, err := a.companion.SubmitTurn(companion.SubmitTurnRequest{
		ConversationID: request.ConversationID, Input: request.Input,
		TraceID: request.TraceID, MessageSource: request.MessageSource,
		ReplyIntent: intent, RecentTargetReply: request.RecentTargetReply,
		PersonNoteSenderIDs: append([]string(nil), request.PersonNoteSenderIDs...),
	})
	return initiative.TurnOutcome{ResponseText: outcome.ResponseText}, err
}

func (a initiativeAdapter) ScheduleDesktopInitiation(conversationID string, evidenceIDs []string, observation session.DesktopObservation) error {
	if a.companion == nil {
		return companion.ErrTurnRuntimeUnavailable
	}
	return a.companion.ScheduleDesktopInitiation(companion.DesktopInitiationRequest{
		ConversationID: conversationID, ObservationEvidenceIDs: append([]string(nil), evidenceIDs...),
	}, observation)
}

func (a initiativeAdapter) BeginMessageTrace(source, conversationID, traceID string) string {
	if traceID != "" || a.messages == nil {
		return traceID
	}
	return a.messages.Begin(source, conversationID)
}

func (a initiativeAdapter) EndMessageTrace(traceID, status string) {
	if traceID != "" && a.messages != nil {
		a.messages.End(traceID, status)
	}
}

func (a initiativeAdapter) EmitParticipation(event initiative.Event) {
	if a.events != nil {
		a.events.Publish(event)
	}
}

func (a initiativeAdapter) RecordParticipation(traceIDs []string, targetTraceID, action string) {
	if a.messages != nil {
		a.messages.Participation(traceIDs, targetTraceID, action)
	}
}

func (a initiativeAdapter) WarnInitiative(message, conversationID string, generation uint64, err error) {
	a.warn(message, conversationID, "", generation, err)
}

func (a initiativeAdapter) warn(message, conversationID, turnID string, generation uint64, err error) {
	if a.logger == nil {
		return
	}
	fields := []zap.Field{zap.String("conversationId", conversationID)}
	if turnID != "" {
		fields = append(fields, zap.String("turnId", turnID))
	}
	if generation != 0 {
		fields = append(fields, zap.Uint64("generation", generation))
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
	}
	a.logger.Warn(message, fields...)
}

type ambientReplyAdapter struct {
	service *initiative.Service
}

func (a ambientReplyAdapter) ObserveAmbientReply(reply companion.AmbientReply) {
	if a.service == nil {
		return
	}
	a.service.ObserveAmbientReply(initiative.FeedbackRegistration{
		CharacterID: reply.CharacterID, ConversationID: reply.ConversationID,
		TurnID: reply.TurnID, EntryIDs: append([]string(nil), reply.EntryIDs...), ReplyText: reply.ReplyText,
	})
}
