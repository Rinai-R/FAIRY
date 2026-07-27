package memory

import (
	"fairy/session"
	"strings"
	"testing"
)

func TestOpenEndpointConversationRejectsInvalidKeyBeforeDatabase(t *testing.T) {
	store := &Store{}
	tests := []struct {
		character string
		binding   session.Binding
		digest    string
	}{
		{"", desktopBinding(), strings.Repeat("a", 64)},
		{"character", session.Binding{}, strings.Repeat("a", 64)},
		{"character", desktopBinding(), "short"},
		{"character", desktopBinding(), strings.Repeat("A", 64)},
	}
	for _, test := range tests {
		if _, err := store.OpenOrCreateEndpointConversation(test.character, test.binding, test.digest); err == nil {
			t.Fatalf("OpenOrCreateEndpointConversation(%q, %#v, %q) succeeded", test.character, test.binding, test.digest)
		}
	}
}

func desktopBinding() session.Binding {
	return session.Binding{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
			Presentation: session.PresentationEmbodied,
		},
	}
}
