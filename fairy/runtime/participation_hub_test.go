package runtime

import (
	"errors"
	"testing"

	"fairy/companion"
)

func TestParticipationHubPublishesByConversation(t *testing.T) {
	hub := NewParticipationHub()
	t.Cleanup(hub.Close)
	subscription := hub.Subscribe("c1")
	hub.Publish(companion.ParticipationEvent{ConversationID: "c2", Action: "silent"})
	hub.Publish(companion.ParticipationEvent{ConversationID: "c1", Action: "wait"})
	select {
	case event := <-subscription.Events:
		if event.Action != "wait" {
			t.Fatalf("action = %q", event.Action)
		}
	default:
		t.Fatal("expected participation event")
	}
}

func TestParticipationHubFailsSlowSubscriber(t *testing.T) {
	hub := NewParticipationHub()
	subscription := hub.Subscribe("c1")
	for generation := uint64(1); generation <= participationHubBuffer+1; generation++ {
		hub.Publish(companion.ParticipationEvent{ConversationID: "c1", Generation: generation})
	}
	if err := <-subscription.Failures; !errors.Is(err, ErrParticipationSubscriberOverflow) {
		t.Fatalf("failure = %v", err)
	}
}
