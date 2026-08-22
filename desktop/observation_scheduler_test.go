package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func testObservationScheduler(t *testing.T, limit, failures int, evaluate observationEvaluator) *observationScheduler {
	t.Helper()
	scheduler, err := newObservationScheduler(observationSchedulerConfig{
		Interval: time.Minute, DailyEvaluationLimit: limit, ConsecutiveFailureLimit: failures,
	}, evaluate)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func TestObservationSchedulerCoalescesBusyTriggers(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	scheduler := testObservationScheduler(t, 10, 3, func(context.Context) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})
	done := make(chan observationTriggerResult, 1)
	go func() { done <- scheduler.Trigger(t.Context()) }()
	<-started
	if got := scheduler.Trigger(t.Context()); got != observationCoalesced {
		t.Fatalf("second trigger = %q", got)
	}
	if got := scheduler.Trigger(t.Context()); got != observationCoalesced {
		t.Fatalf("third trigger = %q", got)
	}
	close(release)
	if got := <-done; got != observationTriggered {
		t.Fatalf("owner trigger = %q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("evaluation calls = %d, want 2", calls.Load())
	}
}

func TestObservationSchedulerEnforcesDailyBudgetAndRollsDay(t *testing.T) {
	var calls atomic.Int32
	scheduler := testObservationScheduler(t, 2, 3, func(context.Context) error { calls.Add(1); return nil })
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	scheduler.now = func() time.Time { return now }
	if scheduler.Trigger(t.Context()) != observationTriggered || scheduler.Trigger(t.Context()) != observationTriggered {
		t.Fatal("eligible evaluation was not triggered")
	}
	if got := scheduler.Trigger(t.Context()); got != observationBudgetExhausted {
		t.Fatalf("budget trigger = %q", got)
	}
	now = now.Add(24 * time.Hour)
	if got := scheduler.Trigger(t.Context()); got != observationTriggered {
		t.Fatalf("next-day trigger = %q", got)
	}
	if calls.Load() != 3 {
		t.Fatalf("evaluation calls = %d, want 3", calls.Load())
	}
}

func TestObservationSchedulerSuspendsAfterConsecutiveFailures(t *testing.T) {
	var calls atomic.Int32
	scheduler := testObservationScheduler(t, 10, 2, func(context.Context) error {
		calls.Add(1)
		return errors.New("capture unavailable")
	})
	if got := scheduler.Trigger(t.Context()); got != observationTriggered {
		t.Fatalf("first trigger = %q", got)
	}
	if got := scheduler.Trigger(t.Context()); got != observationSuspended {
		t.Fatalf("second trigger = %q", got)
	}
	if snapshot := scheduler.Snapshot(); !snapshot.Suspended || snapshot.ConsecutiveFailures != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if got := scheduler.Trigger(t.Context()); got != observationSuspended || calls.Load() != 2 {
		t.Fatalf("suspended trigger = %q, calls = %d", got, calls.Load())
	}
	scheduler.Resume()
	if snapshot := scheduler.Snapshot(); snapshot.Suspended || snapshot.ConsecutiveFailures != 0 {
		t.Fatalf("resumed snapshot = %#v", snapshot)
	}
}
