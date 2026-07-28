package companion

import (
	"context"
	"errors"
	"fmt"
)

func (e *TurnEngine) CancelTurn(conversationID string, turnID string) error {
	s := e.host
	if s == nil || !s.TurnRuntimeReady() {
		return ErrTurnRuntimeUnavailable
	}
	if conversationID == "" || turnID == "" {
		return errors.New("conversation_id and turn_id are required")
	}
	if s.turnRegistry == nil {
		return ErrTurnNotActive
	}
	if err := s.turnRegistry.Cancel(conversationID, turnID); err != nil {
		if errors.Is(err, ErrTurnNotActive) {
			return ErrTurnNotActive
		}
		return fmt.Errorf("cancelling desktop tool execution: %w", err)
	}
	return nil
}

func mapModelCancelError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return ErrTurnInterrupted
	}
	if errors.Is(err, context.Canceled) {
		return ErrTurnInterrupted
	}
	return err
}
