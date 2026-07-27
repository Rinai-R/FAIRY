package companion

import (
	"context"
	"sync"
	"time"
)

// CancelHook lets the owner of a turn-specific external execution cancel it
// without making this lifecycle package depend on Companion or desktopcapture.
type turnCancelHook func(context.Context, string, string) error

type activeTurn struct {
	turnID     string
	cancel     context.CancelFunc
	delivering bool
}

type turnGate struct {
	mu         sync.Mutex
	activeTurn *activeTurn
	compacting bool
}

type turnRegistry struct {
	mu         sync.Mutex
	gates      map[string]*turnGate
	cancelHook turnCancelHook
}

func newTurnRegistry(cancelHook turnCancelHook) *turnRegistry {
	return &turnRegistry{gates: make(map[string]*turnGate), cancelHook: cancelHook}
}

func (r *turnRegistry) gateFor(conversationID string) *turnGate {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gates == nil {
		r.gates = make(map[string]*turnGate)
	}
	g := r.gates[conversationID]
	if g == nil {
		g = &turnGate{}
		r.gates[conversationID] = g
	}
	return g
}

// Reserve acquires the conversation slot before persistence.
func (r *turnRegistry) Reserve(conversationID string) (context.Context, error) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn != nil || g.compacting {
		return nil, ErrTurnInProgress
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.activeTurn = &activeTurn{cancel: cancel}
	return ctx, nil
}

func (r *turnRegistry) Bind(conversationID, turnID string) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn != nil {
		g.activeTurn.turnID = turnID
	}
}

func (r *turnRegistry) MarkDelivering(conversationID, turnID string) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn != nil && g.activeTurn.turnID == turnID {
		g.activeTurn.delivering = true
	}
}

// CancelBeforeDelivery cancels only a turn that has not entered final delivery.
func (r *turnRegistry) CancelBeforeDelivery(conversationID string) (bool, error) {
	g := r.lookup(conversationID)
	if g == nil {
		return false, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn == nil || g.activeTurn.delivering {
		return false, nil
	}
	var hookErr error
	if r.cancelHook != nil && g.activeTurn.turnID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		hookErr = r.cancelHook(ctx, conversationID, g.activeTurn.turnID)
		cancel()
	}
	g.activeTurn.cancel()
	return true, hookErr
}

// Cancel cancels a specific active turn and its external execution.
func (r *turnRegistry) Cancel(conversationID, turnID string) error {
	g := r.lookup(conversationID)
	if g == nil {
		return ErrTurnNotActive
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn == nil || g.activeTurn.turnID != turnID {
		return ErrTurnNotActive
	}
	if r.cancelHook != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := r.cancelHook(ctx, conversationID, turnID)
		cancel()
		if err != nil {
			return err
		}
	}
	g.activeTurn.cancel()
	return nil
}

func (r *turnRegistry) End(conversationID, turnID string) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn == nil {
		return
	}
	if turnID != "" && g.activeTurn.turnID != "" && g.activeTurn.turnID != turnID {
		return
	}
	g.activeTurn.cancel()
	g.activeTurn = nil
}

func (r *turnRegistry) BeginCompaction(conversationID string) error {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn != nil || g.compacting {
		return ErrTurnInProgress
	}
	g.compacting = true
	return nil
}

func (r *turnRegistry) EndCompaction(conversationID string) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	g.compacting = false
	g.mu.Unlock()
}

func (r *turnRegistry) CancelAll() {
	r.mu.Lock()
	gates := make([]*turnGate, 0, len(r.gates))
	for _, g := range r.gates {
		gates = append(gates, g)
	}
	r.mu.Unlock()
	for _, g := range gates {
		g.mu.Lock()
		if g.activeTurn != nil {
			g.activeTurn.cancel()
			g.activeTurn = nil
		}
		g.mu.Unlock()
	}
}

func (r *turnRegistry) lookup(conversationID string) *turnGate {
	r.mu.Lock()
	g := r.gates[conversationID]
	r.mu.Unlock()
	return g
}
