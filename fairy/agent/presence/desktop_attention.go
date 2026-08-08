package presence

import (
	"errors"
	"sync"
	"time"
)

type AttentionState struct {
	lastInitiated time.Time
	budgetWindow  time.Time
	used          int
	expiresAt     time.Time
}

type AttentionEvaluator struct {
	mu       sync.Mutex
	states   map[string]AttentionState
	capacity int
}

const maxDesktopAttentionStates = 256

func NewAttentionEvaluator() *AttentionEvaluator {
	return newAttentionEvaluator(maxDesktopAttentionStates)
}

func newAttentionEvaluator(capacity int) *AttentionEvaluator {
	if capacity < 1 {
		capacity = 1
	}
	return &AttentionEvaluator{
		states:   make(map[string]AttentionState, capacity),
		capacity: capacity,
	}
}

func (e *AttentionEvaluator) Evaluate(conversationID string, desired DesktopObservationAction, rulebook DesktopRulebook, now time.Time) (DesktopObservationAction, error) {
	if e == nil {
		return DesktopActionSilent, errors.New("desktop attention evaluator is unavailable")
	}
	if desired != DesktopActionInitiate {
		return desired, nil
	}
	if rulebook.AttentionBudget <= 0 {
		return DesktopActionSilent, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pruneExpiredLocked(now)
	state, found := e.states[conversationID]
	if !found && len(e.states) >= e.capacity {
		return DesktopActionSilent, nil
	}
	next := state
	if next.budgetWindow.IsZero() || now.Sub(next.budgetWindow) >= time.Hour {
		next.budgetWindow, next.used = now, 0
	}
	spacingDeadline := next.lastInitiated.Add(rulebook.MinSpacing)
	if next.used >= rulebook.AttentionBudget || (!next.lastInitiated.IsZero() && now.Before(spacingDeadline)) {
		if found {
			if deadline := attentionStateExpiry(state.budgetWindow, spacingDeadline); deadline.After(state.expiresAt) {
				state.expiresAt = deadline
				e.states[conversationID] = state
			}
		}
		return DesktopActionSilent, nil
	}
	next.used++
	next.lastInitiated = now
	next.expiresAt = attentionStateExpiry(next.budgetWindow, now.Add(rulebook.MinSpacing))
	e.states[conversationID] = next
	return DesktopActionInitiate, nil
}

func (e *AttentionEvaluator) pruneExpiredLocked(now time.Time) {
	for conversationID, state := range e.states {
		if state.expiresAt.IsZero() || !now.Before(state.expiresAt) {
			delete(e.states, conversationID)
		}
	}
}

func attentionStateExpiry(budgetWindow, spacingDeadline time.Time) time.Time {
	budgetDeadline := budgetWindow.Add(time.Hour)
	if spacingDeadline.After(budgetDeadline) {
		return spacingDeadline
	}
	return budgetDeadline
}

func (e *AttentionEvaluator) Clear() {
	if e == nil {
		return
	}
	e.mu.Lock()
	clear(e.states)
	e.mu.Unlock()
}
