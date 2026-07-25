package observation

import (
	"testing"
	"time"
)

func privateDesktopRulebook(now time.Time) DesktopRulebook {
	return DesktopRulebook{Resolved: Resolved{Endpoint: EndpointDesktop, Facts: Facts{Audience: AudienceSingle}, Memory: MemoryPersonal}, Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyNormal, AllowsKnowledge: true, AllowsPersonalMemory: true, AllowsPlanner: true, AllowsInitiation: true, AttentionBudget: 1, MinSpacing: time.Minute, Now: now}
}

func TestCompileDesktopGraphSelectsConditionalPrivateNodes(t *testing.T) {
	now := time.UnixMilli(100000)
	plan, err := CompileDesktopGraph(privateDesktopRulebook(now), DesktopObservation{ObservationID: "obs-1", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerLifecycle, Activity: DesktopActivityIdle, Lifecycle: DesktopLifecycleReturned, Privacy: DesktopPrivacyNormal})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != DesktopActionInitiate || len(plan.Nodes) != 5 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.Nodes[4].Kind != DesktopNodeInitiate {
		t.Fatalf("nodes = %#v", plan.Nodes)
	}
}

func TestCompileDesktopGraphUsesCurrentTimeWhenRulebookNowIsUnset(t *testing.T) {
	now := time.Now()
	plan, err := CompileDesktopGraph(DesktopRulebook{
		Resolved:             Resolved{Endpoint: EndpointDesktop, Facts: Facts{Audience: AudienceSingle}, Memory: MemoryPersonal},
		AllowsPersonalMemory: true, AllowsKnowledge: true, AttentionBudget: 1,
	}, DesktopObservation{ObservationID: "obs-now", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyNormal})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Nodes) < 2 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestCompileDesktopGraphSuppressesPrivateNodesForPublicOrPrivacy(t *testing.T) {
	now := time.UnixMilli(100000)
	rulebook := privateDesktopRulebook(now)
	rulebook.Resolved.Memory = MemoryPublic
	if _, err := CompileDesktopGraph(rulebook, DesktopObservation{ObservationID: "obs-1", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyNormal}); err == nil {
		t.Fatal("public graph accepted")
	}
	rulebook = privateDesktopRulebook(now)
	plan, err := CompileDesktopGraph(rulebook, DesktopObservation{ObservationID: "obs-2", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyLocked})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != DesktopActionSilent || len(plan.Nodes) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
}
