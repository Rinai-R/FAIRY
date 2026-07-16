package companion

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrTurnInProgress  = errors.New("TURN_IN_PROGRESS: companion turn or compaction already in progress")
	ErrTurnInterrupted = errors.New("TURN_INTERRUPTED: companion turn was cancelled")
	ErrTurnNotActive   = errors.New("TURN_NOT_ACTIVE: no matching active turn to cancel")
)

type activeTurn struct {
	turnID string
	cancel context.CancelFunc
}

type conversationGate struct {
	mu         sync.Mutex
	activeTurn *activeTurn
	compacting bool
}

func (s *CompanionService) gateFor(conversationID string) *conversationGate {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	if s.gates == nil {
		s.gates = make(map[string]*conversationGate)
	}
	gate := s.gates[conversationID]
	if gate == nil {
		gate = &conversationGate{}
		s.gates[conversationID] = gate
	}
	return gate
}

// reserveTurn acquires the conversation turn slot before persistence so concurrent
// SubmitCompiledTurn calls fail with TURN_IN_PROGRESS without writing a second user turn.
func (s *CompanionService) reserveTurn(conversationID string) (context.Context, error) {
	gate := s.gateFor(conversationID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.activeTurn != nil || gate.compacting {
		return nil, ErrTurnInProgress
	}
	ctx, cancel := context.WithCancel(context.Background())
	gate.activeTurn = &activeTurn{cancel: cancel}
	return ctx, nil
}

func (s *CompanionService) bindTurn(conversationID string, turnID string) {
	gate := s.gateFor(conversationID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.activeTurn != nil {
		gate.activeTurn.turnID = turnID
	}
}

func (s *CompanionService) endTurn(conversationID string, turnID string) {
	gate := s.gateFor(conversationID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.activeTurn == nil {
		return
	}
	if turnID != "" && gate.activeTurn.turnID != "" && gate.activeTurn.turnID != turnID {
		return
	}
	gate.activeTurn.cancel()
	gate.activeTurn = nil
}

func (s *CompanionService) beginCompaction(conversationID string) error {
	gate := s.gateFor(conversationID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.activeTurn != nil || gate.compacting {
		return ErrTurnInProgress
	}
	gate.compacting = true
	return nil
}

func (s *CompanionService) endCompaction(conversationID string) {
	gate := s.gateFor(conversationID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.compacting = false
}

// CancelTurn cancels an in-flight compiled turn for the conversation.
func (s *CompanionService) CancelTurn(conversationID string, turnID string) error {
	if s == nil || !s.RespondRuntimeMigrated() {
		return ErrRespondRuntimeNotMigrated
	}
	if conversationID == "" || turnID == "" {
		return errors.New("conversation_id and turn_id are required")
	}
	s.gateMu.Lock()
	gate := s.gates[conversationID]
	s.gateMu.Unlock()
	if gate == nil {
		return ErrTurnNotActive
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.activeTurn == nil || gate.activeTurn.turnID != turnID {
		return ErrTurnNotActive
	}
	gate.activeTurn.cancel()
	return nil
}

func mapModelCancelError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return ErrTurnInterrupted
	}
	return err
}
