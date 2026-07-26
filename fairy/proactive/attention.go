package proactive

import (
	"errors"
	"sync"
	"time"

	appobs "fairy/observation"
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

func (e *AttentionEvaluator) Evaluate(conversationID string, plan appobs.DesktopGraphPlan, rulebook appobs.DesktopRulebook, now time.Time) (appobs.DesktopObservationAction, error) {
	if e == nil {
		return appobs.DesktopActionSilent, errors.New("desktop attention evaluator is unavailable")
	}
	if plan.Action == appobs.DesktopActionSilent {
		return plan.Action, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.states[conversationID]
	if state.budgetWindow.IsZero() || now.Sub(state.budgetWindow) >= time.Hour {
		state.budgetWindow, state.used = now, 0
	}
	if state.used >= rulebook.AttentionBudget || (!state.lastInitiated.IsZero() && now.Sub(state.lastInitiated) < rulebook.MinSpacing) {
		return appobs.DesktopActionSilent, nil
	}
	if plan.Action == appobs.DesktopActionInitiate {
		state.used++
		state.lastInitiated = now
	}
	e.states[conversationID] = state
	return plan.Action, nil
}
