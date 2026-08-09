//go:build live

package presence

import (
	"context"
	"time"
)

// LiveEvalAmbientBatch is an immutable projection of an Inbox decision batch.
// It exists only in live-evaluation builds and never exposes Inbox state.
type LiveEvalAmbientBatch struct {
	ConversationID   string
	Generation       uint64
	EvaluationReason ParticipationEvaluationReason
	Messages         []AmbientObservation
	CacheMessages    []AmbientObservation
}

// SetLiveEvalDecisionHook replaces only the decision call used by Inbox. The
// projected slices are cloned for every callback so a live evaluator cannot
// mutate the rolling Inbox window.
func (a *Inbox) SetLiveEvalDecisionHook(hook func(context.Context, LiveEvalAmbientBatch) (ParticipationResult, error)) {
	if a == nil {
		return
	}
	if hook == nil {
		a.decideHook = nil
		return
	}
	a.decideHook = func(ctx context.Context, batch ambientBatch) (ParticipationResult, error) {
		return hook(ctx, liveEvalBatch(batch))
	}
}

// SetLiveEvalSubmitHook replaces only the Turn submission used by Inbox.
func (a *Inbox) SetLiveEvalSubmitHook(hook func(TurnRequest) (TurnOutcome, error)) {
	if a == nil {
		return
	}
	a.submitHook = hook
}

// SetLiveEvalMaximumTimerDelay caps wait decisions without changing production
// timing. It must be configured before observations are submitted.
func (a *Inbox) SetLiveEvalMaximumTimerDelay(maximum time.Duration) {
	if a == nil || maximum <= 0 {
		return
	}
	original := a.after
	a.after = func(delay time.Duration, callback func()) stoppableTimer {
		if delay > maximum {
			delay = maximum
		}
		return original(delay, callback)
	}
}

// LiveEvalIdle reports whether one conversation has no active decision or
// timer. It is a snapshot and does not expose the underlying state object.
func (a *Inbox) LiveEvalIdle(conversationID string) bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.states[conversationID]
	return state == nil || (!state.running && state.timer == nil && state.decisionCancel == nil)
}

func liveEvalBatch(batch ambientBatch) LiveEvalAmbientBatch {
	return LiveEvalAmbientBatch{
		ConversationID:   batch.conversationID,
		Generation:       batch.generation,
		EvaluationReason: batch.evaluationReason,
		Messages:         cloneLiveEvalObservations(batch.messages),
		CacheMessages:    cloneLiveEvalObservations(batch.cacheMessages),
	}
}

func cloneLiveEvalObservations(observations []AmbientObservation) []AmbientObservation {
	cloned := append([]AmbientObservation(nil), observations...)
	for index := range cloned {
		cloned[index].Mentions = append(cloned[index].Mentions[:0:0], cloned[index].Mentions...)
	}
	return cloned
}
