package companion

import (
	contracts "fairy/contracts/interaction"
	obs "fairy/contracts/observation"
	appobs "fairy/observation"
	"testing"
	"time"
)

func TestObserveDesktopRuleTreeShortCircuitsPrivacyBeforeGraph(t *testing.T) {
	service := NewCompanionService()
	if err := service.BindInteraction("conversation-private", contracts.Binding{
		Endpoint: contracts.EndpointDesktop,
		Facts:    contracts.Facts{Audience: contracts.AudienceSingle, Initiation: contracts.InitiationDirect, Presentation: contracts.PresentationEmbodied},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	plan, err := service.ObserveDesktop("conversation-private", DesktopObservation{
		ObservationID: "obs-privacy", TimestampUnixMS: now.UnixMilli(), Trigger: obs.DesktopTriggerLifecycle,
		Lifecycle: obs.DesktopLifecycleReturned, Privacy: obs.DesktopPrivacyLocked,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != appobs.DesktopActionSilent || len(plan.Nodes) != 0 || len(plan.OmitReasons) != 1 || plan.OmitReasons[0] != "rule_tree:privacy_silent" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestObserveDesktopExecutesTypedGraphAndAppliesAttentionBudget(t *testing.T) {
	service := NewCompanionService()
	if err := service.BindInteraction("conversation-private", contracts.Binding{
		Endpoint: contracts.EndpointDesktop,
		Facts:    contracts.Facts{Audience: contracts.AudienceSingle, Initiation: contracts.InitiationDirect, Presentation: contracts.PresentationEmbodied},
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	first, err := service.ObserveDesktop("conversation-private", DesktopObservation{
		ObservationID: "obs-first", TimestampUnixMS: now.UnixMilli(), Trigger: obs.DesktopTriggerLifecycle,
		Lifecycle: obs.DesktopLifecycleReturned, Privacy: obs.DesktopPrivacyNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Action != appobs.DesktopActionInitiate {
		t.Fatalf("first action = %q", first.Action)
	}
	if len(first.Diagnostics) == 0 || first.Diagnostics[0].Node != "normalize" || first.Diagnostics[0].Status != "started" {
		t.Fatalf("first diagnostics = %#v", first.Diagnostics)
	}

	second, err := service.ObserveDesktop("conversation-private", DesktopObservation{
		ObservationID: "obs-second", TimestampUnixMS: now.Add(time.Millisecond).UnixMilli(), Trigger: obs.DesktopTriggerLifecycle,
		Lifecycle: obs.DesktopLifecycleReturned, Privacy: obs.DesktopPrivacyNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Action != appobs.DesktopActionSilent {
		t.Fatalf("second action = %q", second.Action)
	}
	for _, diagnostic := range second.Diagnostics {
		if diagnostic.Node == "planner" || diagnostic.Node == "respond" || diagnostic.Node == "persist" {
			t.Fatalf("silent execution reached %q: %#v", diagnostic.Node, second.Diagnostics)
		}
	}
}
