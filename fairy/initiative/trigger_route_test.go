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

func TestRouteTriggerDirectlyAppliesPrivacyAndEntryRules(t *testing.T) {
	now := time.Now()
	private := session.Resolved{Endpoint: session.EndpointDesktop, Facts: session.Facts{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}, Principal: session.PrincipalOwner, Memory: session.MemoryPersonal}
	observation := DesktopObservation{ObservationID: "obs-1", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic, Privacy: DesktopPrivacyNormal}
	envelope := TriggerEnvelope{
		Kind: TriggerDesktopObservation, ConversationID: "conversation-1", Resolved: private,
		Payload: DesktopObservationTriggerPayload{Observation: observation}, CreatedAt: now,
	}
	route, err := RouteTrigger(envelope, DesktopPrivacyNormal, true, now)
	if err != nil || route.Pipeline != PipelineObservation || route.Branch != "observation" {
		t.Fatalf("normal RouteTrigger() = %#v, %v", route, err)
	}
	route, err = RouteTrigger(envelope, DesktopPrivacyLocked, true, now)
	if err != nil || route.Pipeline != PipelineSilent || route.Branch != "privacy_silent" {
		t.Fatalf("privacy RouteTrigger() = %#v, %v", route, err)
	}
	if _, err := RouteTrigger(envelope, DesktopPrivacyNormal, false, now); err == nil {
		t.Fatal("RouteTrigger accepted invalid evidence")
	}
}

func TestRouteTriggerValidatesBeforeSelectingPublicEntry(t *testing.T) {
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
