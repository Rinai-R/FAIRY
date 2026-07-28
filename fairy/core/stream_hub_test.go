package core

import (
	"errors"
	"testing"
)

func TestHubFansOutByKeyAndFailsOnlySlowSubscriber(t *testing.T) {
	overflow := errors.New("overflow")
	hub := newStreamHub[string, int](1, 3, overflow, errors.New("capacity"))
	defer hub.Close()
	slow, err := hub.Subscribe("a")
	if err != nil {
		t.Fatal(err)
	}
	fast, err := hub.Subscribe("a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := hub.Subscribe("b")
	if err != nil {
		t.Fatal(err)
	}
	hub.Publish("a", 1)
	if got := <-fast.Events; got != 1 {
		t.Fatalf("fast event = %d", got)
	}
	hub.Publish("a", 2)
	if err := <-slow.Failures; !errors.Is(err, overflow) {
		t.Fatalf("slow failure = %v", err)
	}
	if got := <-fast.Events; got != 2 {
		t.Fatalf("fast event = %d", got)
	}
	select {
	case <-other.Events:
		t.Fatal("cross-key event delivered")
	default:
	}
}

func TestHubCloseAndUnsubscribeAreIdempotent(t *testing.T) {
	hub := newStreamHub[string, int](1, 2, errors.New("overflow"), errors.New("capacity"))
	sub, err := hub.Subscribe("a")
	if err != nil {
		t.Fatal(err)
	}
	sub.Unsubscribe()
	sub.Unsubscribe()
	hub.Close()
	hub.Close()
	if hub.SubscriberCount() != 0 {
		t.Fatal("streamSubscriber remains after close")
	}
}

func TestHubBoundsSubscribersAndRecoversAfterRelease(t *testing.T) {
	capacityErr := errors.New("capacity")
	hub := newStreamHub[string, int](1, 2, errors.New("overflow"), capacityErr)
	first, err := hub.Subscribe("a")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Unsubscribe()
	second, err := hub.Subscribe("b")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Unsubscribe()
	if _, err := hub.Subscribe("c"); !errors.Is(err, capacityErr) {
		t.Fatalf("overload error = %v, want capacity", err)
	}
	if got := hub.SubscriberCount(); got != 2 {
		t.Fatalf("subscriber count = %d, want 2", got)
	}
	first.Unsubscribe()
	replacement, err := hub.Subscribe("c")
	if err != nil {
		t.Fatalf("Subscribe after release error = %v", err)
	}
	defer replacement.Unsubscribe()
	if got := hub.SubscriberCount(); got != 2 {
		t.Fatalf("subscriber count after replacement = %d, want 2", got)
	}
}
