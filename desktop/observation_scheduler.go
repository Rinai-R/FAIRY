package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

type observationEvaluator func(context.Context) error

type observationSchedulerConfig struct {
	Interval                time.Duration
	DailyEvaluationLimit    int
	ConsecutiveFailureLimit int
}

type observationSchedulerSnapshot struct {
	Active              bool
	Pending             bool
	Suspended           bool
	EvaluationsToday    int
	ConsecutiveFailures int
}

type observationTriggerResult string

const (
	observationTriggered       observationTriggerResult = "triggered"
	observationCoalesced       observationTriggerResult = "coalesced"
	observationBudgetExhausted observationTriggerResult = "budget_exhausted"
	observationSuspended       observationTriggerResult = "suspended"
)

type observationScheduler struct {
	mu       sync.Mutex
	config   observationSchedulerConfig
	evaluate observationEvaluator
	now      func() time.Time
	owner    observationOwner

	suspended           bool
	budgetDay           time.Time
	evaluationsToday    int
	consecutiveFailures int
}

// observationOwner serializes evaluations and retains at most one rerun while
// an evaluation is already active.
type observationOwner struct {
	mu      sync.Mutex
	active  bool
	pending bool
}

func (o *observationOwner) Start() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active {
		o.pending = true
		return false
	}
	o.active = true
	return true
}

func (o *observationOwner) Finish() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending {
		o.pending = false
		return true
	}
	o.active = false
	return false
}

func (o *observationOwner) Abort() {
	o.mu.Lock()
	o.active = false
	o.pending = false
	o.mu.Unlock()
}

func (o *observationOwner) Snapshot() (bool, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active, o.pending
}

func newObservationScheduler(config observationSchedulerConfig, evaluate observationEvaluator) (*observationScheduler, error) {
	if config.Interval <= 0 {
		return nil, errors.New("observation scheduler interval must be positive")
	}
	if config.DailyEvaluationLimit <= 0 {
		return nil, errors.New("observation scheduler daily limit must be positive")
	}
	if config.ConsecutiveFailureLimit <= 0 {
		return nil, errors.New("observation scheduler failure limit must be positive")
	}
	if evaluate == nil {
		return nil, errors.New("observation scheduler evaluator is required")
	}
	return &observationScheduler{config: config, evaluate: evaluate, now: time.Now}, nil
}

func (s *observationScheduler) Run(ctx context.Context) error {
	if s == nil {
		return errors.New("observation scheduler is unavailable")
	}
	if ctx == nil {
		return errors.New("observation scheduler context is required")
	}
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			go s.Trigger(ctx)
		}
	}
}

// Trigger runs the evaluation owner synchronously. Concurrent triggers return
// immediately and retain at most one pending evaluation for that owner.
func (s *observationScheduler) Trigger(ctx context.Context) observationTriggerResult {
	if s == nil || ctx == nil {
		return observationSuspended
	}
	s.mu.Lock()
	if s.suspended {
		s.mu.Unlock()
		return observationSuspended
	}
	s.rollBudgetDayLocked(s.now())
	if s.evaluationsToday >= s.config.DailyEvaluationLimit {
		s.mu.Unlock()
		return observationBudgetExhausted
	}
	s.mu.Unlock()

	if !s.owner.Start() {
		return observationCoalesced
	}

	result := observationTriggered
	for {
		s.mu.Lock()
		s.rollBudgetDayLocked(s.now())
		if s.suspended || s.evaluationsToday >= s.config.DailyEvaluationLimit {
			if s.suspended {
				result = observationSuspended
			} else {
				result = observationBudgetExhausted
			}
			s.mu.Unlock()
			s.owner.Abort()
			return result
		}
		s.evaluationsToday++
		s.mu.Unlock()

		err := s.evaluate(ctx)

		s.mu.Lock()
		if err != nil {
			s.consecutiveFailures++
			if s.consecutiveFailures >= s.config.ConsecutiveFailureLimit {
				s.suspended = true
				result = observationSuspended
			}
		} else {
			s.consecutiveFailures = 0
		}
		aborted := s.suspended
		s.mu.Unlock()
		if aborted {
			s.owner.Abort()
			return result
		}
		if !s.owner.Finish() {
			return result
		}
	}
}

func (s *observationScheduler) Resume() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.suspended = false
	s.consecutiveFailures = 0
	s.mu.Unlock()
	s.owner.Abort()
}

func (s *observationScheduler) Snapshot() observationSchedulerSnapshot {
	if s == nil {
		return observationSchedulerSnapshot{Suspended: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollBudgetDayLocked(s.now())
	active, pending := s.owner.Snapshot()
	return observationSchedulerSnapshot{
		Active: active, Pending: pending, Suspended: s.suspended,
		EvaluationsToday: s.evaluationsToday, ConsecutiveFailures: s.consecutiveFailures,
	}
}

func (s *observationScheduler) rollBudgetDayLocked(now time.Time) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if s.budgetDay.IsZero() || !s.budgetDay.Equal(day) {
		s.budgetDay = day
		s.evaluationsToday = 0
	}
}
