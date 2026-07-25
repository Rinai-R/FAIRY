package companion

import (
	contracts "fairy/contracts/interaction"
	"strings"
	"testing"
)

func TestObserveAmbientRejectsDirectIMSynchronously(t *testing.T) {
	service := NewCompanionService()
	AttachOwnerIdentityStore(service, staticAmbientOwner(false))
	defer service.Close()
	if err := service.BindInteraction("c1", contracts.Binding{
		Endpoint: contracts.EndpointIM,
		Facts: contracts.Facts{
			Audience: contracts.AudienceSingle, Initiation: contracts.InitiationDirect,
			Presentation: contracts.PresentationChat, PrincipalNamespace: "qq.onebot",
			PrincipalDigest: strings.Repeat("a", 64),
		},
	}); err != nil {
		t.Fatal(err)
	}
	err := service.ObserveAmbient("c1", AmbientObservation{MessageID: "m1", SenderID: "u1", SenderName: "n", Text: "t", TimestampUnixMS: 1})
	if err == nil || !strings.Contains(err.Error(), "initiation=ambient") {
		t.Fatalf("ObserveAmbient() error = %v", err)
	}
}

type staticAmbientOwner bool

func (owner staticAmbientOwner) IsOwner(string, string) (bool, error) {
	return bool(owner), nil
}
