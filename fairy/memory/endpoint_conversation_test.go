package memory

import (
	"strings"
	"testing"

	"fairy/interaction"
)

func TestOpenEndpointConversationRejectsInvalidKeyBeforeDatabase(t *testing.T) {
	store := &Store{}
	tests := []struct {
		character string
		binding   interaction.Binding
		digest    string
	}{
		{"", desktopBinding(), strings.Repeat("a", 64)},
		{"character", interaction.Binding{}, strings.Repeat("a", 64)},
		{"character", desktopBinding(), "short"},
		{"character", desktopBinding(), strings.Repeat("A", 64)},
	}
	for _, test := range tests {
		if _, err := store.OpenOrCreateEndpointConversation(test.character, test.binding, test.digest); err == nil {
			t.Fatalf("OpenOrCreateEndpointConversation(%q, %#v, %q) succeeded", test.character, test.binding, test.digest)
		}
	}
}

func desktopBinding() interaction.Binding {
	return interaction.Binding{
		Endpoint: interaction.EndpointDesktop,
		Facts: interaction.Facts{
			Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect,
			Presentation: interaction.PresentationEmbodied,
		},
	}
}
