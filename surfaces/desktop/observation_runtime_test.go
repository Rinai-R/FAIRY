package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fairy/session"
)

func TestDesktopObservationRuntimeKeepsSamplingAndSubmittingSeparate(t *testing.T) {
	var sampled, submitted atomic.Int32
	runtime, err := newDesktopObservationRuntime(
		func(context.Context) (session.DesktopObservation, error) {
			sampled.Add(1)
			return session.DesktopObservation{ObservationID: "obs-runtime", TimestampUnixMS: time.Now().UnixMilli(), Trigger: session.DesktopTriggerPeriodic, Activity: session.DesktopActivityWorking, Privacy: session.DesktopPrivacyNormal}, nil
		},
		func(context.Context, session.DesktopObservation) error { submitted.Add(1); return nil },
		observationSchedulerConfig{Interval: time.Hour, DailyEvaluationLimit: 2, ConsecutiveFailureLimit: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sampled.Load() != 1 || submitted.Load() != 1 {
		t.Fatalf("sampled=%d submitted=%d", sampled.Load(), submitted.Load())
	}
}

func TestDesktopObservationRuntimeRetainsQueueUntilSubmitSucceeds(t *testing.T) {
	submitErr := errors.New("offline")
	var attempts atomic.Int32
	runtime, err := newDesktopObservationRuntime(
		func(context.Context) (session.DesktopObservation, error) {
			return session.DesktopObservation{ObservationID: "obs-retry", TimestampUnixMS: time.Now().UnixMilli(), Trigger: session.DesktopTriggerPeriodic, Activity: session.DesktopActivityWorking, Privacy: session.DesktopPrivacyNormal}, nil
		},
		func(context.Context, session.DesktopObservation) error {
			if attempts.Add(1) == 1 {
				return submitErr
			}
			return nil
		},
		observationSchedulerConfig{Interval: time.Hour, DailyEvaluationLimit: 2, ConsecutiveFailureLimit: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.evaluate(context.Background()); !errors.Is(err, submitErr) {
		t.Fatalf("first evaluation error = %v", err)
	}
	queued, ok := runtime.policy.Next(time.Now())
	if !ok || queued.ObservationID != "obs-retry" {
		t.Fatalf("queued = %#v, ok=%v", queued, ok)
	}
	if err := runtime.evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.policy.Next(time.Now()); ok {
		t.Fatal("queue retained acknowledged observation")
	}
}
