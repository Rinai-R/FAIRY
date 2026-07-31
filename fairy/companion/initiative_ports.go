package companion

import (
	"time"

	"fairy/memory"
	"fairy/session"
)

// DesktopEvidenceValidator is consumed by Companion to defend initiation
// admission. Core supplies Initiative's evidence registry.
type DesktopEvidenceValidator interface {
	ContainsFresh(id string, now time.Time) bool
}

func AttachDesktopEvidenceValidator(service *CompanionService, validator DesktopEvidenceValidator) {
	if service == nil {
		return
	}
	service.desktopEvidence = validator
}

type AmbientReply struct {
	CharacterID    string
	ConversationID string
	TurnID         string
	Candidates     []memory.SocialFeedbackCandidate
	ReplyText      string
}

// AmbientReplyObserver receives a completed public reply so Initiative can
// attribute later social feedback without Companion importing Initiative.
type AmbientReplyObserver interface {
	ObserveAmbientReply(AmbientReply)
}

func AttachAmbientReplyObserver(service *CompanionService, observer AmbientReplyObserver) {
	if service == nil {
		return
	}
	service.ambientReplies = observer
}

func (s *CompanionService) CancelTurnBeforeDelivery(conversationID string) {
	if s != nil {
		s.cancelTurnBeforeDelivery(conversationID)
	}
}

func (s *CompanionService) ScheduleDesktopInitiation(request DesktopInitiationRequest, observation session.DesktopObservation) error {
	if s == nil || s.retention == nil {
		return ErrTurnRuntimeUnavailable
	}
	if err := s.retention.run(func() {
		_, err := s.SubmitDesktopInitiation(request, observation)
		if err != nil {
			s.setBackgroundError(err)
		}
	}); err != nil {
		return err
	}
	return nil
}

func socialMemoryFeedbackCandidates(context memory.SocialMemoryContext) []memory.SocialFeedbackCandidate {
	candidates := make([]memory.SocialFeedbackCandidate, 0, len(context.Entries))
	for _, entry := range context.Entries {
		candidates = append(candidates, memory.SocialFeedbackCandidate{
			ID: entry.ID, Kind: entry.Kind, Situation: entry.Situation,
			Content: entry.Content, RecallCue: entry.RecallCue,
		})
	}
	return candidates
}
