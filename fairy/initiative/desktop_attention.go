package initiative

import (
	"errors"
	"sync"
	"time"
)

type AttentionState struct {
	lastInitiated time.Time
	budgetWindow  time.Time
	used          int
}

type AttentionEvaluator struct {
	mu     sync.Mutex
	states map[string]AttentionState
}

func NewAttentionEvaluator() *AttentionEvaluator {
	return &AttentionEvaluator{states: make(map[string]AttentionState)}
}

func (e *AttentionEvaluator) Evaluate(conversationID string, plan DesktopGraphPlan, rulebook DesktopRulebook, now time.Time) (DesktopObservationAction, error) {
	if e == nil {
		return DesktopActionSilent, errors.New("desktop attention evaluator is unavailable")
	}
	if plan.Action == DesktopActionSilent {
		return plan.Action, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.states[conversationID]
	if state.budgetWindow.IsZero() || now.Sub(state.budgetWindow) >= time.Hour {
		state.budgetWindow, state.used = now, 0
	}
	if state.used >= rulebook.AttentionBudget || (!state.lastInitiated.IsZero() && now.Sub(state.lastInitiated) < rulebook.MinSpacing) {
		return DesktopActionSilent, nil
	}
	if plan.Action == DesktopActionInitiate {
		state.used++
		state.lastInitiated = now
	}
	e.states[conversationID] = state
	return plan.Action, nil
}
