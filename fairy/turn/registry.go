// Package turn owns per-conversation admission and cancellation state.
package turn

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrInProgress = errors.New("TURN_IN_PROGRESS: companion turn or compaction already in progress")
	ErrNotActive  = errors.New("TURN_NOT_ACTIVE: no matching active turn to cancel")
)

// CancelHook lets the owner of a turn-specific external execution cancel it
// without making this lifecycle package depend on Companion or desktopcapture.
type CancelHook func(context.Context, string, string) error

type active struct {
	turnID     string
	cancel     context.CancelFunc
	delivering bool
}

type gate struct {
	mu         sync.Mutex
	activeTurn *active
	compacting bool
}

type Registry struct {
	mu         sync.Mutex
	gates      map[string]*gate
	cancelHook CancelHook
}

func NewRegistry(cancelHook CancelHook) *Registry {
	return &Registry{gates: make(map[string]*gate), cancelHook: cancelHook}
}

func (r *Registry) gateFor(conversationID string) *gate {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gates == nil {
		r.gates = make(map[string]*gate)
	}
	g := r.gates[conversationID]
	if g == nil {
		g = &gate{}
		r.gates[conversationID] = g
	}
	return g
}

// Reserve acquires the conversation slot before persistence.
func (r *Registry) Reserve(conversationID string) (context.Context, error) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn != nil || g.compacting {
		return nil, ErrInProgress
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.activeTurn = &active{cancel: cancel}
	return ctx, nil
}

func (r *Registry) Bind(conversationID, turnID string) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn != nil {
		g.activeTurn.turnID = turnID
	}
}

func (r *Registry) MarkDelivering(conversationID, turnID string) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn != nil && g.activeTurn.turnID == turnID {
		g.activeTurn.delivering = true
	}
}

// CancelBeforeDelivery cancels only a turn that has not entered final delivery.
func (r *Registry) CancelBeforeDelivery(conversationID string) (bool, error) {
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
func (r *Registry) Cancel(conversationID, turnID string) error {
	g := r.lookup(conversationID)
	if g == nil {
		return ErrNotActive
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn == nil || g.activeTurn.turnID != turnID {
		return ErrNotActive
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

func (r *Registry) End(conversationID, turnID string) {
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

func (r *Registry) BeginCompaction(conversationID string) error {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.activeTurn != nil || g.compacting {
		return ErrInProgress
	}
	g.compacting = true
	return nil
}

func (r *Registry) EndCompaction(conversationID string) {
	g := r.gateFor(conversationID)
	g.mu.Lock()
	g.compacting = false
	g.mu.Unlock()
}

func (r *Registry) CancelAll() {
	r.mu.Lock()
	gates := make([]*gate, 0, len(r.gates))
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

func (r *Registry) lookup(conversationID string) *gate {
	r.mu.Lock()
	g := r.gates[conversationID]
	r.mu.Unlock()
	return g
}
