package conversation

import (
	"time"

	"fairy/context/social"
	"fairy/transport/session"
)

// DesktopEvidenceValidator is consumed by Companion to defend initiation
// admission. Core supplies Initiative's evidence registry.
type DesktopEvidenceValidator interface {
	ContainsFresh(id string, now time.Time) bool
}

func AttachDesktopEvidenceValidator(service *Service, validator DesktopEvidenceValidator) {
	if service == nil {
		return
	}
	service.desktopEvidence = validator
}

type AmbientReply struct {
	CharacterID    string
	ConversationID string
	TurnID         string
	Candidates     []social.SocialFeedbackCandidate
	ReplyText      string
}

// AmbientReplyObserver receives a completed public reply so Initiative can
// attribute later social feedback without Companion importing Initiative.
type AmbientReplyObserver interface {
	ObserveAmbientReply(AmbientReply)
}

// DeferredTurnScheduler is the asynchronous admission boundary used by
// proactive Presence domain. Core supplies it independently from RetentionPort.
type DeferredTurnScheduler interface {
	ScheduleDeferred(func()) error
}

func AttachDeferredTurnScheduler(service *Service, scheduler DeferredTurnScheduler) {
	if service != nil {
		service.deferredTurns = scheduler
	}
}

func AttachAmbientReplyObserver(service *Service, observer AmbientReplyObserver) {
	if service == nil {
		return
	}
	service.ambientReplies = observer
}

func (s *Service) ScheduleDesktopInitiation(request DesktopInitiationRequest, observation session.DesktopObservation) error {
	if s == nil || s.deferredTurns == nil {
		return ErrTurnRuntimeUnavailable
	}
	if err := s.deferredTurns.ScheduleDeferred(func() {
		_, err := s.SubmitDesktopInitiation(request, observation)
		if err != nil {
			s.setBackgroundError(err)
		}
	}); err != nil {
		return err
	}
	return nil
}

func socialMemoryFeedbackCandidates(context social.SocialMemoryContext) []social.SocialFeedbackCandidate {
	candidates := make([]social.SocialFeedbackCandidate, 0, len(context.Entries))
	for _, entry := range context.Entries {
		candidates = append(candidates, social.SocialFeedbackCandidate{
			ID: entry.ID, Kind: entry.Kind, Situation: entry.Situation,
			Content: entry.Content, RecallCue: entry.RecallCue,
		})
	}
	return candidates
}
