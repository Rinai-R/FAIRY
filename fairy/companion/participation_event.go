package companion

import "time"

type ParticipationEvent struct {
	ConversationID   string                        `json:"conversationId"`
	Generation       uint64                        `json:"generation"`
	EvaluationReason ParticipationEvaluationReason `json:"evaluationReason"`
	Action           string                        `json:"action"`
	TargetMessageID  string                        `json:"targetMessageId,omitempty"`
	WaitSeconds      int                           `json:"waitSeconds,omitempty"`
	Usage            []LaneModelUsage              `json:"usage,omitempty"`
	ObservedAt       time.Time                     `json:"observedAt"`
}

type ParticipationEventEmitter func(ParticipationEvent)

func AttachParticipationEventEmitter(service *CompanionService, emit ParticipationEventEmitter) {
	if service == nil {
		return
	}
	service.emitMu.Lock()
	service.emitParticipation = emit
	service.emitMu.Unlock()
}

func (s *CompanionService) emitParticipationEvent(event ParticipationEvent) {
	if s == nil {
		return
	}
	s.emitMu.Lock()
	emit := s.emitParticipation
	s.emitMu.Unlock()
	if emit != nil {
		emit(event)
	}
}
