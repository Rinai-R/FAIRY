//go:build live

package presence

import (
	"context"
	"testing"
	"time"
)

func TestLiveEvalInboxBatchDoesNotLeakMutableState(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	defer inbox.Close()
	seen := make(chan LiveEvalAmbientBatch, 2)
	calls := 0
	inbox.SetLiveEvalDecisionHook(func(_ context.Context, batch LiveEvalAmbientBatch) (ParticipationResult, error) {
		calls++
		if calls == 1 {
			batch.Messages[0].Text = "mutated"
			batch.CacheMessages[0].Text = "mutated"
		}
		seen <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	})
	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	<-seen
	waitUntil(t, func() bool { return inbox.LiveEvalIdle("c1") })
	if err := inbox.Observe("c1", testObservation(2)); err != nil {
		t.Fatal(err)
	}
	batch := <-seen
	if batch.Messages[0].Text == "mutated" || batch.CacheMessages[0].Text == "mutated" {
		t.Fatalf("live projection mutated inbox state: %#v", batch)
	}
}

func TestLiveEvalInboxTimerCapAndIdleConverge(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	defer inbox.Close()
	inbox.SetLiveEvalMaximumTimerDelay(10 * time.Millisecond)
	var calls int
	inbox.SetLiveEvalDecisionHook(func(_ context.Context, _ LiveEvalAmbientBatch) (ParticipationResult, error) {
		calls++
		if calls == 1 {
			wait := 60
			return ParticipationResult{Action: ParticipationWait, WaitSeconds: &wait}, nil
		}
		return ParticipationResult{Action: ParticipationSilent}, nil
	})
	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !inbox.LiveEvalIdle("c1") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !inbox.LiveEvalIdle("c1") || calls != 2 {
		t.Fatalf("idle=%v calls=%d", inbox.LiveEvalIdle("c1"), calls)
	}
}
