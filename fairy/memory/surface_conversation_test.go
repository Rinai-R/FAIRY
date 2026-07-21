package memory

import (
	"strings"
	"testing"
)

func TestOpenSurfaceConversationRejectsInvalidKeyBeforeDatabase(t *testing.T) {
	store := &Store{}
	tests := []struct {
		character string
		surface   string
		digest    string
	}{
		{"", "desktop", strings.Repeat("a", 64)},
		{"character", "other", strings.Repeat("a", 64)},
		{"character", "desktop", "short"},
		{"character", "desktop", strings.Repeat("A", 64)},
	}
	for _, test := range tests {
		if _, err := store.OpenOrCreateSurfaceConversation(test.character, test.surface, test.digest); err == nil {
			t.Fatalf("OpenOrCreateSurfaceConversation(%q, %q, %q) succeeded", test.character, test.surface, test.digest)
		}
	}
}
