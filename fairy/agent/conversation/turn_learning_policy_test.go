package conversation

import (
	"testing"

	"fairy/transport/session"
)

func TestResolveTurnLearningPolicyIsolatesEvaluation(t *testing.T) {
	evaluation := session.Resolved{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
			Presentation: session.PresentationChat, Evaluation: true,
		},
		Principal: session.PrincipalOwner,
		Memory:    session.MemoryPersonal,
	}
	policy := resolveTurnLearningPolicy(evaluation)
	if policy.extraction || policy.social || policy.knowledge {
		t.Fatalf("evaluation learning policy = %#v", policy)
	}

	normal := evaluation
	normal.Facts.Evaluation = false
	policy = resolveTurnLearningPolicy(normal)
	if !policy.extraction || policy.social || !policy.knowledge {
		t.Fatalf("normal private learning policy = %#v", policy)
	}

	public := session.Resolved{
		Endpoint:  session.EndpointIM,
		Facts:     session.Facts{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat},
		Principal: session.PrincipalNone,
		Memory:    session.MemoryPublic,
	}
	policy = resolveTurnLearningPolicy(public)
	if policy.extraction || !policy.social || !policy.knowledge {
		t.Fatalf("public learning policy = %#v", policy)
	}
}
