package core

import (
	"os"
	"strings"
	"testing"
)

func TestEventHubConsumesSessionOwnedEventType(t *testing.T) {
	source, err := os.ReadFile("event_hub.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, `"fairy/session"`) || !strings.Contains(text, "session.Event") {
		t.Fatal("EventHub does not consume session.Event")
	}
	if strings.Contains(text, `"fairy/companion"`) {
		t.Fatal("EventHub imports a lifecycle implementation package")
	}
}
