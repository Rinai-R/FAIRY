package conversation

import (
	"context"

	"fairy/agent/conversation/turngate"
	"fairy/agent/reply"
)

var (
	ErrTurnInProgress  = turngate.ErrTurnInProgress
	ErrTurnInterrupted = reply.ErrInterrupted
	ErrTurnNotActive   = turngate.ErrTurnNotActive
)

// reserveTurn acquires the conversation turn slot before persistence so concurrent
// SubmitCompiledTurn calls fail with TURN_IN_PROGRESS without writing a second user turn.
func (s *Service) reserveTurn(conversationID string) (context.Context, error) {
	if s == nil || s.turnRegistry == nil {
		return nil, ErrTurnRuntimeUnavailable
	}
	return s.turnRegistry.Reserve(conversationID)
}

func (s *Service) bindTurn(conversationID string, turnID string) {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.Bind(conversationID, turnID)
	}
}

func (s *Service) markTurnDelivering(conversationID, turnID string) {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.MarkDelivering(conversationID, turnID)
	}
}

// cancelTurnBeforeDelivery invalidates model generation when newer ambient
// input arrives, but never interrupts a turn that has entered final delivery.
func (s *Service) cancelTurnBeforeDelivery(conversationID string) bool {
	if s == nil || s.turnRegistry == nil {
		return false
	}
	canceled, err := s.turnRegistry.CancelBeforeDelivery(conversationID)
	if err != nil {
		s.setBackgroundError(err)
	}
	return canceled
}

func (s *Service) endTurn(conversationID string, turnID string) {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.End(conversationID, turnID)
	}
}

func (s *Service) beginCompaction(conversationID string) error {
	if s == nil || s.turnRegistry == nil {
		return ErrTurnRuntimeUnavailable
	}
	return s.turnRegistry.BeginCompaction(conversationID)
}

func (s *Service) endCompaction(conversationID string) {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.EndCompaction(conversationID)
	}
}

// CancelTurn cancels an in-flight compiled turn for the conversation.
