package memory

import (
	contracts "fairy/contracts/interaction"
	"strings"
	"testing"
)

func TestOpenEndpointConversationRejectsInvalidKeyBeforeDatabase(t *testing.T) {
	store := &Store{}
	tests := []struct {
		character string
		binding   contracts.Binding
		digest    string
	}{
		{"", desktopBinding(), strings.Repeat("a", 64)},
		{"character", contracts.Binding{}, strings.Repeat("a", 64)},
		{"character", desktopBinding(), "short"},
		{"character", desktopBinding(), strings.Repeat("A", 64)},
	}
	for _, test := range tests {
		if _, err := store.OpenOrCreateEndpointConversation(test.character, test.binding, test.digest); err == nil {
			t.Fatalf("OpenOrCreateEndpointConversation(%q, %#v, %q) succeeded", test.character, test.binding, test.digest)
		}
	}
}

func desktopBinding() contracts.Binding {
	return contracts.Binding{
		Endpoint: contracts.EndpointDesktop,
		Facts: contracts.Facts{
			Audience: contracts.AudienceSingle, Initiation: contracts.InitiationDirect,
			Presentation: contracts.PresentationEmbodied,
		},
	}
}
