package companion

import (
	"context"
	"errors"

	"fairy/reply"
)

var (
	ErrTurnInProgress  = errors.New("TURN_IN_PROGRESS: companion turn or compaction already in progress")
	ErrTurnInterrupted = reply.ErrInterrupted
	ErrTurnNotActive   = errors.New("TURN_NOT_ACTIVE: no matching active turn to cancel")
)

// reserveTurn acquires the conversation turn slot before persistence so concurrent
// SubmitCompiledTurn calls fail with TURN_IN_PROGRESS without writing a second user turn.
func (s *CompanionService) reserveTurn(conversationID string) (context.Context, error) {
	if s == nil || s.turnRegistry == nil {
		return nil, ErrRespondRuntimeNotMigrated
	}
	return s.turnRegistry.Reserve(conversationID)
}

func (s *CompanionService) bindTurn(conversationID string, turnID string) {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.Bind(conversationID, turnID)
	}
}

func (s *CompanionService) markTurnDelivering(conversationID, turnID string) {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.MarkDelivering(conversationID, turnID)
	}
}

// cancelTurnBeforeDelivery invalidates model generation when newer ambient
// input arrives, but never interrupts a turn that has entered final delivery.
func (s *CompanionService) cancelTurnBeforeDelivery(conversationID string) bool {
	if s == nil || s.turnRegistry == nil {
		return false
	}
	canceled, err := s.turnRegistry.CancelBeforeDelivery(conversationID)
	if err != nil {
		s.setBackgroundError(err)
	}
	return canceled
}

func (s *CompanionService) endTurn(conversationID string, turnID string) {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.End(conversationID, turnID)
	}
}

func (s *CompanionService) beginCompaction(conversationID string) error {
	if s == nil || s.turnRegistry == nil {
		return ErrRespondRuntimeNotMigrated
	}
	return s.turnRegistry.BeginCompaction(conversationID)
}

func (s *CompanionService) endCompaction(conversationID string) {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.EndCompaction(conversationID)
	}
}

// CancelTurn cancels an in-flight compiled turn for the conversation.
