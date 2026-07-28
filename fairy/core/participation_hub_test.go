package core

import (
	"errors"
	"testing"

	"fairy/initiative"
)

func TestParticipationHubPublishesByConversation(t *testing.T) {
	hub := NewParticipationHub()
	t.Cleanup(hub.Close)
	subscription, err := hub.Subscribe("c1")
	if err != nil {
		t.Fatal(err)
	}
	hub.Publish(initiative.Event{ConversationID: "c2", Action: "silent"})
	hub.Publish(initiative.Event{ConversationID: "c1", Action: "wait"})
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
	subscription, err := hub.Subscribe("c1")
	if err != nil {
		t.Fatal(err)
	}
	for generation := uint64(1); generation <= participationHubBuffer+1; generation++ {
		hub.Publish(initiative.Event{ConversationID: "c1", Generation: generation})
	}
	if err := <-subscription.Failures; !errors.Is(err, ErrParticipationSubscriberOverflow) {
		t.Fatalf("failure = %v", err)
	}
}

func TestParticipationHubBoundsTotalSubscribersAndRecovers(t *testing.T) {
	hub := NewParticipationHub()
	hub.inner.capacity = 1
	first, err := hub.Subscribe("conversation-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Subscribe("conversation-b"); !errors.Is(err, ErrParticipationSubscriberCapacity) {
		t.Fatalf("overload error = %v", err)
	}
	first.Unsubscribe()
	replacement, err := hub.Subscribe("conversation-b")
	if err != nil {
		t.Fatalf("Subscribe after release error = %v", err)
	}
	replacement.Unsubscribe()
	if got := hub.inner.SubscriberCount(); got != 0 {
		t.Fatalf("subscriber count after cleanup = %d", got)
	}
}
