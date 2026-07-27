package runtime

import (
	"os"
	"strings"
	"testing"
)

func TestEventHubConsumesTurnOwnedEventType(t *testing.T) {
	source, err := os.ReadFile("event_hub.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, `"fairy/turn"`) {
		t.Fatal("EventHub does not import fairy/turn")
	}
	if strings.Contains(text, `"fairy/companion"`) || strings.Contains(text, "companion.TurnEvent") {
		t.Fatal("EventHub still consumes companion.TurnEvent")
	}
}
