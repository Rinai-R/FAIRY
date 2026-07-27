package core

import (
	"errors"
	"testing"
)

func TestHubFansOutByKeyAndFailsOnlySlowSubscriber(t *testing.T) {
	overflow := errors.New("overflow")
	hub := newStreamHub[string, int](1, overflow)
	defer hub.Close()
	slow := hub.Subscribe("a")
	fast := hub.Subscribe("a")
	other := hub.Subscribe("b")
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
	hub := newStreamHub[string, int](1, errors.New("overflow"))
	sub := hub.Subscribe("a")
	sub.Unsubscribe()
	sub.Unsubscribe()
	hub.Close()
	hub.Close()
	if hub.SubscriberCount() != 0 {
		t.Fatal("streamSubscriber remains after close")
	}
}
