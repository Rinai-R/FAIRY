package core

import (
	"context"
	"errors"

	"go.uber.org/zap"

	turn "fairy/agent/conversation"
	initiative "fairy/agent/presence"
	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/model"
	"fairy/transport/session"
	api "fairy/transport/web"
)

type initiativeAdapter struct {
	turns      *turn.Service
	history    *history.Store
	social     *social.Store
	characters *character.CharacterService
	config     *config.Reader
	model      *model.ModelService
	messages   observedMessages
	events     *ParticipationHub
	logger     *zap.Logger
}

type observedMessages interface {
	Begin(source, conversationID string) string
	BeginCorrelated(source, conversationID, messageID string) string
	Participation(traceIDs []string, targetTraceID, action string)
	StartParticipationSpan(traceID, operation, category string, attributes map[string]string) string
	FinishSpan(spanID, status string, attributes map[string]string)
	End(traceID, status string)
}

func (a initiativeAdapter) ResolveInteraction(conversationID string) (session.Resolved, error) {
	if a.turns == nil {
		return session.Resolved{}, turn.ErrTurnRuntimeUnavailable
	}
	return a.turns.ResolveInteraction(conversationID)
}

func (a initiativeAdapter) LoadConversationActivity(conversationID string, nowUnixMS int64) (history.ConversationActivity, error) {
	if a.history == nil {
		return history.ConversationActivity{}, turn.ErrTurnRuntimeUnavailable
	}
	return a.history.LoadConversationActivity(conversationID, nowUnixMS)
}

func (a initiativeAdapter) LoadConversationRecord(conversationID string) (history.ConversationRecord, error) {
	if a.history == nil {
		return history.ConversationRecord{}, turn.ErrTurnRuntimeUnavailable
	}
	return a.history.LoadConversationRecord(conversationID)
}

func (a initiativeAdapter) ActiveCharacter(characterID string) (character.Record, error) {
	if a.characters == nil || a.characters.CatalogStore() == nil {
		return character.Record{}, turn.ErrTurnRuntimeUnavailable
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

func (a initiativeAdapter) ListSocialPersonNotes(ctx context.Context, characterID, conversationID string, senderIDs []string) ([]social.SocialPersonNote, error) {
	if a.social == nil {
		return nil, turn.ErrTurnRuntimeUnavailable
	}
	return a.social.ListSocialPersonNotes(ctx, characterID, conversationID, senderIDs)
}

func (a initiativeAdapter) RetrieveSocialMemoryContext(ctx context.Context, characterID, conversationID, query string) (social.SocialMemoryContext, error) {
	if a.social == nil {
		return social.SocialMemoryContext{}, turn.ErrTurnRuntimeUnavailable
	}
	return a.social.RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
}

func (a initiativeAdapter) ModelConnection() (config.ModelConnection, error) {
	if a.config == nil {
		return config.ModelConnection{}, turn.ErrTurnRuntimeUnavailable
	}
	return a.config.ModelConnection()
}

func (a initiativeAdapter) ExecuteRequest(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	if a.model == nil {
		return nil, turn.ErrTurnRuntimeUnavailable
	}
	return a.model.ExecuteRequestContext(ctx, request)
}

func (a initiativeAdapter) StoreSocialMemoryEntries(ctx context.Context, input social.SocialMemoryBatchInput) ([]social.SocialMemoryEntry, error) {
	if a.social == nil {
		return nil, turn.ErrTurnRuntimeUnavailable
	}
	return a.social.StoreSocialMemoryEntries(ctx, input)
}

func (a initiativeAdapter) UpsertSocialPersonNote(ctx context.Context, input social.SocialPersonNoteInput) (social.SocialPersonNote, error) {
	if a.social == nil {
		return social.SocialPersonNote{}, turn.ErrTurnRuntimeUnavailable
	}
	return a.social.UpsertSocialPersonNote(ctx, input)
}

func (a initiativeAdapter) RecordSocialFeedbackBatch(ctx context.Context, input social.SocialFeedbackBatchInput) (social.SocialFeedbackBatchResult, error) {
	if a.social == nil {
		return social.SocialFeedbackBatchResult{}, turn.ErrTurnRuntimeUnavailable
	}
	return a.social.RecordSocialFeedbackBatch(ctx, input)
}

func (a initiativeAdapter) WarnLearning(conversationID string, err error) {
	a.warn("social learning failed", conversationID, "", 0, err)
}

func (a initiativeAdapter) WarnFeedback(conversationID, turnID string, err error) {
	a.warn("social feedback failed", conversationID, turnID, 0, err)
}

func (a initiativeAdapter) SubmitTurn(request initiative.TurnRequest) (initiative.TurnOutcome, error) {
	if a.turns == nil {
		return initiative.TurnOutcome{}, turn.ErrTurnRuntimeUnavailable
	}
	var intent *turn.ReplyIntent
	if request.ReplyIntent != nil {
		intent = &turn.ReplyIntent{
			ReplyAct: request.ReplyIntent.ReplyAct, Tone: request.ReplyIntent.Tone,
			RelationshipSignal: request.ReplyIntent.RelationshipSignal, ReplyMode: request.ReplyIntent.ReplyMode,
			Focus: request.ReplyIntent.Focus, Avoid: append([]string(nil), request.ReplyIntent.Avoid...),
			ReferenceInfo: request.ReplyIntent.ReferenceInfo, MemoryQuery: request.ReplyIntent.MemoryQuery,
			ExpressionQuery: request.ReplyIntent.ExpressionQuery, DriftLevel: request.ReplyIntent.DriftLevel,
			AnchorPolicy: request.ReplyIntent.AnchorPolicy,
		}
	}
	outcome, err := a.turns.SubmitTurn(turn.SubmitTurnRequest{
		ConversationID: request.ConversationID, Input: request.Input,
		TraceID: request.TraceID, MessageSource: request.MessageSource,
		ReplyTargetMessageID: request.ReplyTargetMessageID,
		ReplyIntent:          intent, RecentTargetReply: request.RecentTargetReply,
		PersonNoteSenderIDs: append([]string(nil), request.PersonNoteSenderIDs...),
	})
	return initiative.TurnOutcome{ResponseText: outcome.ResponseText}, err
}

func (a initiativeAdapter) ScheduleDesktopInitiation(conversationID string, evidenceIDs []string, observation session.DesktopObservation) error {
	if a.turns == nil {
		return turn.ErrTurnRuntimeUnavailable
	}
	return a.turns.ScheduleDesktopInitiation(turn.DesktopInitiationRequest{
		ConversationID: conversationID, ObservationEvidenceIDs: append([]string(nil), evidenceIDs...),
	}, observation)
}

func (a initiativeAdapter) BeginMessageTrace(source, conversationID, messageID, traceID string) string {
	if traceID != "" || a.messages == nil {
		return traceID
	}
	return a.messages.BeginCorrelated(source, conversationID, messageID)
}

func (a initiativeAdapter) EndMessageTrace(traceID, status string) {
	if traceID != "" && a.messages != nil {
		a.messages.End(traceID, status)
	}
}

func (a initiativeAdapter) StartParticipationSpan(traceID, operation, category string, attributes map[string]string) string {
	if traceID == "" || a.messages == nil {
		return ""
	}
	return a.messages.StartParticipationSpan(traceID, operation, category, attributes)
}

func (a initiativeAdapter) FinishParticipationSpan(spanID, status string, attributes map[string]string) {
	if spanID != "" && a.messages != nil {
		a.messages.FinishSpan(spanID, status, attributes)
	}
}

func (a initiativeAdapter) EmitParticipation(event initiative.Event) {
	if a.events != nil {
		a.events.Publish(api.ParticipationEvent{
			ConversationID: event.ConversationID, Generation: event.Generation,
			EvaluationReason: string(event.EvaluationReason), Action: event.Action,
			TargetMessageID: event.TargetMessageID, WaitSeconds: event.WaitSeconds,
			Usage: projectParticipationUsage(event.Usage), ObservedAt: event.ObservedAt,
		})
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

func (a ambientReplyAdapter) ObserveAmbientReply(reply turn.AmbientReply) {
	if a.service == nil {
		return
	}
	a.service.ObserveAmbientReply(initiative.FeedbackRegistration{
		CharacterID: reply.CharacterID, ConversationID: reply.ConversationID,
		TurnID: reply.TurnID, Candidates: append([]social.SocialFeedbackCandidate(nil), reply.Candidates...), ReplyText: reply.ReplyText,
	})
}

func projectParticipationUsage(items []model.LaneModelUsage) []session.LaneModelUsage {
	if len(items) == 0 {
		return nil
	}
	projected := make([]session.LaneModelUsage, len(items))
	for index, item := range items {
		projected[index] = session.LaneModelUsage{
			Lane:          item.Lane,
			HistoryWindow: item.HistoryWindow,
			Usage: session.LaneUsage{
				InputTokens:       item.Usage.InputTokens,
				OutputTokens:      item.Usage.OutputTokens,
				CachedInputTokens: session.CachedTokenObservation{Status: item.Usage.CachedInputTokens.Status, Tokens: item.Usage.CachedInputTokens.Tokens},
				CacheWriteTokens:  session.CachedTokenObservation{Status: item.Usage.CacheWriteTokens.Status, Tokens: item.Usage.CacheWriteTokens.Tokens},
			},
		}
	}
	return projected
}
