package companion

import "fairy/participation"

type ParticipationEvent = participation.Event

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
