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
	EntryIDs       []string
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
		return ErrRespondRuntimeNotMigrated
	}
	if !s.retention.run(func() {
		_, err := s.SubmitDesktopInitiation(request, observation)
		if err != nil {
			s.setBackgroundError(err)
		}
	}) {
		return ErrRespondRuntimeNotMigrated
	}
	return nil
}

func socialMemoryEntryIDs(context memory.SocialMemoryContext) []string {
	ids := make([]string, 0, len(context.Entries))
	for _, entry := range context.Entries {
		ids = append(ids, entry.ID)
	}
	return ids
}
