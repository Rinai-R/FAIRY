package presence

import (
	"testing"
	"time"

	"fairy/transport/session"
)

func privateDesktopRulebook(now time.Time) DesktopRulebook {
	return DesktopRulebook{Resolved: session.Resolved{Endpoint: session.EndpointDesktop, Facts: session.Facts{Audience: session.AudienceSingle}, Memory: session.MemoryPersonal}, Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyNormal, AllowsKnowledge: true, AllowsPersonalMemory: true, AllowsPlanner: true, AllowsInitiation: true, AttentionBudget: 1, MinSpacing: time.Minute, Now: now}
}

func TestDecideDesktopObservationSelectsInitiationAndPreservesWireProjection(t *testing.T) {
	now := time.UnixMilli(100000)
	result, err := DecideDesktopObservation(privateDesktopRulebook(now), DesktopObservation{
		ObservationID: "obs-1", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerLifecycle,
		Activity: DesktopActivityIdle, Lifecycle: DesktopLifecycleReturned, Privacy: DesktopPrivacyNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != DesktopActionInitiate || len(result.Nodes) != 5 {
		t.Fatalf("result = %#v", result)
	}
	if result.Nodes[4].Kind != "initiate" {
		t.Fatalf("nodes = %#v", result.Nodes)
	}
}

func TestDecideDesktopObservationUsesCurrentTimeWhenRulebookNowIsUnset(t *testing.T) {
	now := time.Now()
	result, err := DecideDesktopObservation(DesktopRulebook{
		Resolved:             session.Resolved{Endpoint: session.EndpointDesktop, Facts: session.Facts{Audience: session.AudienceSingle}, Memory: session.MemoryPersonal},
		AllowsPersonalMemory: true, AllowsKnowledge: true, AttentionBudget: 1,
	}, DesktopObservation{ObservationID: "obs-now", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyNormal})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) < 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestDecideDesktopObservationRejectsPublicAndStopsForPrivacy(t *testing.T) {
	now := time.UnixMilli(100000)
	rulebook := privateDesktopRulebook(now)
	rulebook.Resolved.Memory = session.MemoryPublic
	if _, err := DecideDesktopObservation(rulebook, DesktopObservation{ObservationID: "obs-1", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyNormal}); err == nil {
		t.Fatal("public desktop decision was accepted")
	}
	rulebook = privateDesktopRulebook(now)
	result, err := DecideDesktopObservation(rulebook, DesktopObservation{ObservationID: "obs-2", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyLocked})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != DesktopActionSilent || len(result.Nodes) != 2 {
		t.Fatalf("result = %#v", result)
	}
}
