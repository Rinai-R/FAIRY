package main

import (
	"fmt"
	"testing"
	"time"

	obs "fairy/contracts/observation"
)

func TestObservationPolicyDeduplicatesAndSuppressesPrivacy(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	p := &observationPolicy{}
	sample := obs.DesktopObservation{ObservationID: "o1", TimestampUnixMS: now.UnixMilli(), Trigger: obs.DesktopTriggerPeriodic, Activity: obs.DesktopActivityWorking, Privacy: obs.DesktopPrivacyNormal}
	if err := p.Enqueue(sample, now); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue(sample, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	privacy := sample
	privacy.ObservationID = "o2"
	privacy.Privacy = obs.DesktopPrivacyMeeting
	if err := p.Enqueue(privacy, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := p.Drain(now.Add(2 * time.Second)); len(got) != 1 || got[0].ObservationID != "o1" {
		t.Fatalf("drain = %#v", got)
	}
}

func TestObservationPolicyReportsDowngradedPrivacyTransition(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	p := &observationPolicy{}
	transition := obs.DesktopObservation{
		ObservationID: "privacy-on", TimestampUnixMS: now.UnixMilli(), Trigger: obs.DesktopTriggerLifecycle,
		Activity: obs.DesktopActivityUnknown, Lifecycle: obs.DesktopLifecyclePrivacyOn, Privacy: obs.DesktopPrivacyMeeting,
	}
	if err := p.Enqueue(transition, now); err != nil {
		t.Fatal(err)
	}
	queued, ok := p.Next(now)
	if !ok || queued.ObservationID != transition.ObservationID || queued.Activity != obs.DesktopActivityUnknown {
		t.Fatalf("privacy transition = %#v, ok=%v", queued, ok)
	}
}

func TestObservationPolicyBoundsQueueAndDropsOldestInOrder(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	p := &observationPolicy{}
	for index := 0; index < desktopObservationQueueLimit+1; index++ {
		sampledAt := now.Add(time.Duration(index) * (desktopObservationMinInterval + time.Second))
		sample := obs.DesktopObservation{
			ObservationID: fmt.Sprintf("o%d", index), TimestampUnixMS: sampledAt.UnixMilli(),
			Trigger: obs.DesktopTriggerPeriodic, Activity: obs.DesktopActivityWorking, Privacy: obs.DesktopPrivacyNormal,
		}
		if err := p.Enqueue(sample, sampledAt); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.queue) != desktopObservationQueueLimit || p.queue[0].ObservationID != "o1" || p.queue[len(p.queue)-1].ObservationID != "o32" {
		t.Fatalf("bounded queue = %#v", p.queue)
	}
	dropped, _ := p.Counters()
	if dropped != 1 {
		t.Fatalf("dropped = %d", dropped)
	}
}

func TestObservationPolicyDropsStaleQueueItems(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	p := &observationPolicy{}
	sample := obs.DesktopObservation{ObservationID: "old", TimestampUnixMS: now.Add(-desktopObservationFreshness - time.Second).UnixMilli(), Trigger: obs.DesktopTriggerPeriodic, Activity: obs.DesktopActivityIdle, Privacy: obs.DesktopPrivacyNormal}
	if err := p.Enqueue(sample, now); err != nil {
		t.Fatal(err)
	}
	_, stale := p.Counters()
	if stale != 1 {
		t.Fatalf("stale = %d", stale)
	}
}
