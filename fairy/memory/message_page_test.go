package memory

import (
	"math"
	"testing"
)

func TestMessagePageValidationRunsBeforeDatabase(t *testing.T) {
	store := &Store{}
	for _, test := range []struct {
		conversation string
		before       uint64
		limit        int
	}{
		{"", 0, 50},
		{"conversation", math.MaxUint64, 50},
		{"conversation", 0, 0},
		{"conversation", 0, 201},
	} {
		if _, err := store.ListConversationMessagesBefore(test.conversation, test.before, test.limit); err == nil {
			t.Fatalf("ListConversationMessagesBefore(%q,%d,%d) succeeded", test.conversation, test.before, test.limit)
		}
	}
}
