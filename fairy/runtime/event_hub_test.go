package runtime

import (
	"testing"
	"time"

	"fairy/companion"
)

func TestEventHubFanOut(t *testing.T) {
	hub := NewEventHub()
	ch, unsub := hub.Subscribe("c1")
	defer unsub()
	hub.Publish(companion.HarnessEvent{ConversationID: "c1", TurnID: "t1", Sequence: 1})
	select {
	case ev := <-ch:
		if ev.TurnID != "t1" {
			t.Fatalf("event = %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
	hub.Publish(companion.HarnessEvent{ConversationID: "other", TurnID: "x"})
	select {
	case <-ch:
		t.Fatal("received cross-conversation event")
	case <-time.After(50 * time.Millisecond):
	}
}
