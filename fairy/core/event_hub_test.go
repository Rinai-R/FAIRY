package core

import (
	"errors"
	"testing"
	"time"

	"fairy/session"
)

func TestEventHubFanOut(t *testing.T) {
	hub := NewEventHub()
	subscription, err := hub.Subscribe("c1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Unsubscribe()
	hub.Publish(session.Event{ConversationID: "c1", TurnID: "t1", Sequence: 1})
	select {
	case event := <-subscription.Events:
		if event.TurnID != "t1" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
	hub.Publish(session.Event{ConversationID: "other", TurnID: "x"})
	select {
	case <-subscription.Events:
		t.Fatal("received cross-conversation event")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEventHubOverflowFailsOnlySlowSubscriber(t *testing.T) {
	hub := NewEventHub()
	slow, err := hub.Subscribe("c1")
	if err != nil {
		t.Fatal(err)
	}
	fast, err := hub.Subscribe("c1")
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Unsubscribe()
	defer fast.Unsubscribe()

	for sequence := 1; sequence <= eventHubBuffer; sequence++ {
		hub.Publish(session.Event{ConversationID: "c1", Sequence: uint64(sequence)})
		select {
		case event := <-fast.Events:
			if event.Sequence != uint64(sequence) {
				t.Fatalf("fast sequence = %d, want %d", event.Sequence, sequence)
			}
		case <-time.After(time.Second):
			t.Fatal("fast subscriber stalled")
		}
	}
	hub.Publish(session.Event{ConversationID: "c1", Sequence: eventHubBuffer + 1})

	select {
	case err := <-slow.Failures:
		if !errors.Is(err, ErrEventSubscriberOverflow) {
			t.Fatalf("slow failure = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow subscriber did not report overflow")
	}
	select {
	case event := <-fast.Events:
		if event.Sequence != eventHubBuffer+1 {
			t.Fatalf("fast final sequence = %d", event.Sequence)
		}
	case <-time.After(time.Second):
		t.Fatal("fast subscriber lost event after peer overflow")
	}
	if got := hub.SubscriberCount(); got != 1 {
		t.Fatalf("subscriber count = %d, want 1", got)
	}
}

func TestEventHubSubscriberCountAndClose(t *testing.T) {
	hub := NewEventHub()
	subscriptionA, err := hub.Subscribe("conversation-a")
	if err != nil {
		t.Fatal(err)
	}
	subscriptionB, err := hub.Subscribe("conversation-b")
	if err != nil {
		t.Fatal(err)
	}
	if got := hub.SubscriberCount(); got != 2 {
		t.Fatalf("subscriber count = %d", got)
	}
	subscriptionA.Unsubscribe()
	if got := hub.SubscriberCount(); got != 1 {
		t.Fatalf("subscriber count after unsubscribe = %d", got)
	}
	hub.Close()
	hub.Close()
	subscriptionB.Unsubscribe()
	if got := hub.SubscriberCount(); got != 0 {
		t.Fatalf("subscriber count after close = %d", got)
	}
	if _, ok := <-subscriptionB.Events; ok {
		t.Fatal("events channel remained open")
	}
	if _, ok := <-subscriptionB.Failures; ok {
		t.Fatal("failures channel remained open")
	}
}

func TestEventHubBoundsTotalSubscribersAndRecovers(t *testing.T) {
	hub := NewEventHub()
	hub.inner.capacity = 2
	first, err := hub.Subscribe("conversation-a")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Unsubscribe()
	second, err := hub.Subscribe("conversation-b")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Unsubscribe()
	if _, err := hub.Subscribe("conversation-c"); !errors.Is(err, ErrEventSubscriberCapacity) {
		t.Fatalf("overload error = %v", err)
	}
	if got := hub.SubscriberCount(); got != 2 {
		t.Fatalf("subscriber count = %d, want 2", got)
	}
	first.Unsubscribe()
	replacement, err := hub.Subscribe("conversation-c")
	if err != nil {
		t.Fatalf("Subscribe after release error = %v", err)
	}
	defer replacement.Unsubscribe()
	if got := hub.SubscriberCount(); got != 2 {
		t.Fatalf("subscriber count after replacement = %d, want 2", got)
	}
}

func BenchmarkEventHubPublishWithoutSubscribers(b *testing.B) {
	hub := NewEventHub()
	defer hub.Close()
	event := session.Event{ConversationID: "conversation-1", TurnID: "turn-1", Sequence: 1}
	b.ReportAllocs()
	for b.Loop() {
		hub.Publish(event)
	}
}
