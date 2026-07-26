package observation

import (
	"testing"
	"time"

	"fairy/interaction"
)

func TestTriggerEnvelopeValidatesEndpointPayloadPair(t *testing.T) {
	now := time.Now()
	private := interaction.Resolved{Endpoint: interaction.EndpointDesktop, Facts: interaction.Facts{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied}, Principal: interaction.PrincipalOwner, Memory: interaction.MemoryPersonal}
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
	tree, err := NewCoreRuleTree()
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

func TestRouteCoreTriggerValidatesBeforeSelectingPipeline(t *testing.T) {
	now := time.Now()
	private := interaction.Resolved{Endpoint: interaction.EndpointDesktop, Facts: interaction.Facts{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied}, Principal: interaction.PrincipalOwner, Memory: interaction.MemoryPersonal}
	route, err := RouteCoreTrigger(TriggerEnvelope{
		Kind: TriggerDirect, ConversationID: "conversation-1", Resolved: private,
		Payload: DirectTrigger{Input: "hello"}, CreatedAt: now,
	}, DesktopPrivacyNormal, true, now)
	if err != nil || route.Pipeline != PipelineDirectTurn || route.Branch != "direct" {
		t.Fatalf("RouteCoreTrigger() = %#v, %v", route, err)
	}

	_, err = RouteCoreTrigger(TriggerEnvelope{
		Kind: TriggerDirect, ConversationID: "conversation-1", Resolved: private,
		Payload: DirectTrigger{}, CreatedAt: now,
	}, DesktopPrivacyNormal, true, now)
	if err == nil {
		t.Fatal("RouteCoreTrigger accepted an empty direct trigger")
	}
}
