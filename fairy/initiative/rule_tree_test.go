package initiative

import (
	"testing"
	"time"

	"fairy/session"
)

func TestTriggerEnvelopeValidatesEndpointPayloadPair(t *testing.T) {
	now := time.Now()
	private := session.Resolved{Endpoint: session.EndpointDesktop, Facts: session.Facts{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}, Principal: session.PrincipalOwner, Memory: session.MemoryPersonal}
	observation := DesktopObservation{ObservationID: "obs-1", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyNormal}
	envelope := TriggerEnvelope{Kind: TriggerDesktopObservation, ConversationID: "conversation-1", Resolved: private, Payload: DesktopObservationTriggerPayload{Observation: observation}, CreatedAt: now}
	if err := envelope.Validate(now); err != nil {
		t.Fatal(err)
	}
	envelope.Kind = TriggerPublicAmbient
	if err := envelope.Validate(now); err == nil {
		t.Fatal("accepted desktop payload as public trigger")
	}
}

func TestRuleTreeUsesPriorityAndNeverExecutesNodes(t *testing.T) {
	tree, err := NewRuleTree(
		RuleBranch{Name: "privacy", When: func(ctx RuleContext) bool { return ctx.Privacy != DesktopPrivacyNormal }, Then: PipelineSilent},
		RuleBranch{Name: "initiate", When: func(ctx RuleContext) bool { return ctx.AllowsInitiation && ctx.AttentionBudget > 0 }, Then: PipelineInitiate},
		RuleBranch{Name: "observe", When: func(RuleContext) bool { return true }, Then: PipelineObservation},
	)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, branch, err := tree.Evaluate(RuleContext{Privacy: DesktopPrivacyLocked, AttentionBudget: 1, AllowsInitiation: true})
	if err != nil || pipeline != PipelineSilent || branch != "privacy" {
		t.Fatalf("Evaluate() = %q, %q, %v", pipeline, branch, err)
	}
	pipeline, branch, err = tree.Evaluate(RuleContext{Privacy: DesktopPrivacyNormal, AttentionBudget: 1, AllowsInitiation: true})
	if err != nil || pipeline != PipelineInitiate || branch != "initiate" {
		t.Fatalf("Evaluate() = %q, %q, %v", pipeline, branch, err)
	}
}

func TestRuleTreeIsImmutableAfterConstruction(t *testing.T) {
	tree, err := NewRuleTree(RuleBranch{Name: "fallback", When: func(RuleContext) bool { return true }, Then: PipelineSilent})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tree.Evaluate(RuleContext{}); err != nil {
		t.Fatal(err)
	}
}

func TestCoreRuleTreeRoutesObservationWithoutDecidingAttentionOutcome(t *testing.T) {
	tree, err := NewInitiativeRuleTree()
	if err != nil {
		t.Fatal(err)
	}
	ctx := RuleContext{
		Trigger: TriggerEnvelope{Kind: TriggerDesktopObservation}, Privacy: DesktopPrivacyNormal,
		AttentionBudget: 1, AllowsInitiation: true, TriggerValid: true, EvidenceValid: true,
	}
	pipeline, branch, err := tree.Evaluate(ctx)
	if err != nil || pipeline != PipelineObservation || branch != "observation" {
		t.Fatalf("Evaluate() = %q, %q, %v", pipeline, branch, err)
	}
	ctx.Privacy = DesktopPrivacyLocked
	pipeline, branch, err = tree.Evaluate(ctx)
	if err != nil || pipeline != PipelineSilent || branch != "privacy_silent" {
		t.Fatalf("privacy Evaluate() = %q, %q, %v", pipeline, branch, err)
	}
	ctx.TriggerValid = false
	pipeline, branch, err = tree.Evaluate(ctx)
	if err != nil || pipeline != PipelineReject || branch != "reject_invalid" {
		t.Fatalf("invalid Evaluate() = %q, %q, %v", pipeline, branch, err)
	}
}

func TestRouteTriggerValidatesBeforeSelectingPipeline(t *testing.T) {
	now := time.Now()
	public := session.Resolved{Endpoint: session.EndpointIM, Facts: session.Facts{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat}, Principal: session.PrincipalNone, Memory: session.MemoryPublic}
	route, err := RouteTrigger(TriggerEnvelope{
		Kind: TriggerPublicAmbient, ConversationID: "conversation-1", Resolved: public,
		Payload: PublicAmbientTrigger{MessageID: "message-1"}, CreatedAt: now,
	}, DesktopPrivacyNormal, true, now)
	if err != nil || route.Pipeline != PipelineParticipation || route.Branch != "public_ambient" {
		t.Fatalf("RouteTrigger() = %#v, %v", route, err)
	}

	_, err = RouteTrigger(TriggerEnvelope{
		Kind: TriggerPublicAmbient, ConversationID: "conversation-1", Resolved: public,
		Payload: PublicAmbientTrigger{}, CreatedAt: now,
	}, DesktopPrivacyNormal, true, now)
	if err == nil {
		t.Fatal("RouteTrigger accepted an empty public ambient trigger")
	}
}
