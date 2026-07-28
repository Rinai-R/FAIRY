package initiative

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fairy/session"
)

type staticInteractionResolver struct {
	resolved session.Resolved
	err      error
}

func (r staticInteractionResolver) ResolveInteraction(string) (session.Resolved, error) {
	return r.resolved, r.err
}

type recordingTurnStarter struct {
	scheduled []session.DesktopObservation
}

func (*recordingTurnStarter) CancelTurnBeforeDelivery(string) {}

func (*recordingTurnStarter) SubmitTurn(TurnRequest) (TurnOutcome, error) {
	return TurnOutcome{}, errors.New("unexpected ambient turn")
}

func (s *recordingTurnStarter) ScheduleDesktopInitiation(_ string, _ []string, observation session.DesktopObservation) error {
	s.scheduled = append(s.scheduled, observation)
	return nil
}

func TestServiceObserveAmbientRejectsDirectInteractionSynchronously(t *testing.T) {
	service := NewService(t.Context(), ServiceOptions{
		Interactions: staticInteractionResolver{resolved: session.Resolved{
			Endpoint: session.EndpointIM,
			Facts: session.Facts{
				Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
				Presentation: session.PresentationChat,
			},
			Principal: session.PrincipalExternal,
			Memory:    session.MemoryPublic,
		}},
	})
	t.Cleanup(service.Close)
	err := service.ObserveAmbient("c1", AmbientObservation{
		MessageID: "m1", SenderID: "u1", SenderName: "n", Text: "t", TimestampUnixMS: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "initiation=ambient") {
		t.Fatalf("ObserveAmbient() error = %v", err)
	}
}

func TestServiceObserveDesktopShortCircuitsPrivacyBeforeDecision(t *testing.T) {
	service := NewService(t.Context(), ServiceOptions{
		Interactions: staticInteractionResolver{resolved: privateDesktopInteraction()},
	})
	t.Cleanup(service.Close)
	now := time.Now()
	plan, err := service.ObserveDesktop("conversation-private", session.DesktopObservation{
		ObservationID: "obs-privacy", TimestampUnixMS: now.UnixMilli(), Trigger: session.DesktopTriggerLifecycle,
		Lifecycle: session.DesktopLifecycleReturned, Privacy: session.DesktopPrivacyLocked,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != DesktopActionSilent || len(plan.Nodes) != 0 || len(plan.OmitReasons) != 1 || plan.OmitReasons[0] != "rule_tree:privacy_silent" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestServiceObserveDesktopAppliesAttentionBudgetAndSchedulesInitiation(t *testing.T) {
	turns := &recordingTurnStarter{}
	service := NewService(t.Context(), ServiceOptions{
		Turns: turns, Interactions: staticInteractionResolver{resolved: privateDesktopInteraction()},
	})
	t.Cleanup(service.Close)
	now := time.Now()
	first, err := service.ObserveDesktop("conversation-private", session.DesktopObservation{
		ObservationID: "obs-first", TimestampUnixMS: now.UnixMilli(), Trigger: session.DesktopTriggerLifecycle,
		Lifecycle: session.DesktopLifecycleReturned, Privacy: session.DesktopPrivacyNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Action != DesktopActionInitiate || len(turns.scheduled) != 1 {
		t.Fatalf("first plan=%#v scheduled=%d", first, len(turns.scheduled))
	}
	if len(first.Diagnostics) == 0 || first.Diagnostics[0].Node != "normalize" || first.Diagnostics[0].Status != "started" {
		t.Fatalf("first diagnostics = %#v", first.Diagnostics)
	}

	second, err := service.ObserveDesktop("conversation-private", session.DesktopObservation{
		ObservationID: "obs-second", TimestampUnixMS: now.Add(time.Millisecond).UnixMilli(), Trigger: session.DesktopTriggerLifecycle,
		Lifecycle: session.DesktopLifecycleReturned, Privacy: session.DesktopPrivacyNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Action != DesktopActionSilent || len(turns.scheduled) != 1 {
		t.Fatalf("second plan=%#v scheduled=%d", second, len(turns.scheduled))
	}
}

func TestServiceCloseStopsInitiativeWorkers(t *testing.T) {
	service := NewService(t.Context(), ServiceOptions{})
	if action, err := service.attention.Evaluate(
		"conversation-1",
		DesktopActionInitiate,
		DesktopRulebook{AttentionBudget: 1, MinSpacing: time.Minute},
		time.UnixMilli(100000),
	); err != nil || action != DesktopActionInitiate {
		t.Fatalf("attention setup = %s, %v", action, err)
	}
	service.Close()
	if got := attentionStateCount(service.attention); got != 0 {
		t.Fatalf("attention states after Close = %d, want 0", got)
	}
	if service.learning.Enqueue(LearningSnapshot{
		ConversationID: "conversation-1",
		Messages:       []AmbientObservation{{MessageID: "m1"}},
	}) {
		t.Fatal("learning enqueue accepted after Service.Close")
	}
	if service.feedback.Register(FeedbackRegistration{
		ConversationID: "conversation-1", TurnID: "turn-1", ReplyText: "reply",
	}) {
		t.Fatal("feedback registration accepted after Service.Close")
	}
}

func privateDesktopInteraction() session.Resolved {
	return session.Resolved{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
			Presentation: session.PresentationEmbodied,
		},
		Principal: session.PrincipalOwner,
		Memory:    session.MemoryPersonal,
	}
}
