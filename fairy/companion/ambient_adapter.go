package companion

import (
	"context"
	"errors"

	"fairy/participation"
	"fairy/sociallearning"

	"go.uber.org/zap"

	domain "fairy/interaction"
)

type AmbientInbox = participation.Inbox

func newAmbientInbox(parent context.Context, service *CompanionService) *AmbientInbox {
	return participation.NewInbox(parent, ambientHost{service: service})
}

func (s *CompanionService) ObserveAmbient(conversationID string, observation AmbientObservation) error {
	if s == nil || s.ambient == nil {
		return errors.New("ambient inbox is not configured")
	}
	resolved, err := s.ResolveInteraction(conversationID)
	if err != nil {
		return err
	}
	if !resolved.AllowsAmbientParticipation() {
		return errors.New("ambient observation requires initiation=ambient")
	}
	if resolved.Memory != domain.MemoryPublic {
		return errors.New("ambient observation requires memory_policy=public")
	}
	return s.ambient.Observe(conversationID, observation)
}

type ambientHost struct {
	service *CompanionService
}

func (h ambientHost) BeginMessageTrace(source, conversationID, traceID string) string {
	return h.service.beginMessageTrace(source, conversationID, traceID)
}

func (h ambientHost) ObserveSocialFeedback(conversationID string, observation AmbientObservation) {
	if h.service != nil && h.service.socialFeedback != nil {
		h.service.socialFeedback.Observe(conversationID, observation)
	}
}

func (h ambientHost) EnqueueSocialLearning(conversationID string, messages []AmbientObservation) {
	if h.service != nil && h.service.socialLearning != nil {
		h.service.socialLearning.Enqueue(sociallearning.LearningSnapshot{ConversationID: conversationID, Messages: messages})
	}
}

func (h ambientHost) CancelTurnBeforeDelivery(conversationID string) {
	if h.service != nil {
		h.service.cancelTurnBeforeDelivery(conversationID)
	}
}

func (h ambientHost) DecideParticipation(ctx context.Context, request participation.ParticipationRequest) (participation.ParticipationResult, error) {
	return h.service.DecideParticipation(ctx, request)
}

func (h ambientHost) SubmitTurn(request participation.TurnRequest) (participation.TurnOutcome, error) {
	outcome, err := h.service.SubmitTurn(SubmitTurnRequest{
		ConversationID: request.ConversationID, Input: request.Input,
		TraceID: request.TraceID, MessageSource: request.MessageSource,
		ReplyIntent: request.ReplyIntent, RecentTargetReply: request.RecentTargetReply,
		PersonNoteSenderIDs: request.PersonNoteSenderIDs,
	})
	return participation.TurnOutcome{ResponseText: outcome.ResponseText}, err
}

func (h ambientHost) EndMessageTrace(traceID, status string) {
	if h.service != nil {
		h.service.endMessageTrace(traceID, status)
	}
}

func (h ambientHost) EmitParticipation(event participation.Event) {
	if h.service == nil {
		return
	}
	h.service.emitParticipationEvent(ParticipationEvent(event))
}

func (h ambientHost) RecordParticipation(traceIDs []string, targetTraceID, action string) {
	if h.service == nil {
		return
	}
	h.service.emitMu.Lock()
	telemetry := h.service.messageTelemetry
	h.service.emitMu.Unlock()
	if telemetry != nil {
		telemetry.Participation(traceIDs, targetTraceID, action)
	}
}

func (h ambientHost) WarnAmbient(message, conversationID string, generation uint64, err error) {
	if h.service == nil || h.service.logger == nil {
		return
	}
	fields := []zap.Field{zap.String("conversationId", conversationID), zap.Uint64("generation", generation)}
	if err != nil {
		fields = append(fields, zap.Error(err))
	}
	h.service.logger.Warn(message, fields...)
}
