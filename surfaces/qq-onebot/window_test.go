package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"fairy/coreclient"
)

func TestGroupWindowStartsFirstMessageImmediately(t *testing.T) {
	batches := make(chan groupWindowBatch, 1)
	w, err := newGroupWindow(t.Context(), func(_ context.Context, batch groupWindowBatch) (groupWindowDecision, error) {
		batches <- batch
		return groupWindowDecision{GroupParticipationResponse: coreclient.GroupParticipationResponse{Action: "silent"}}, nil
	}, noGroupReply, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Add(20001, testObservation(1), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-batches:
		if batch.evaluationReason != "message" || len(batch.messages) != 1 || !batch.messages[0].IsNew {
			t.Fatalf("batch = %#v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("first message did not start participation immediately")
	}
}

func TestGroupWindowDiscardsStaleReplyKeepsLatestTwentyAndSerializes(t *testing.T) {
	started := make(chan groupWindowBatch, 2)
	release := make(chan groupWindowDecision, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	var replies atomic.Int32
	w, err := newGroupWindow(t.Context(), func(_ context.Context, batch groupWindowBatch) (groupWindowDecision, error) {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		started <- batch
		decision := <-release
		active.Add(-1)
		return decision, nil
	}, func(context.Context, groupWindowBatch, groupWindowDecision) error {
		replies.Add(1)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Add(20001, testObservation(0), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	first := receiveBatch(t, started)
	for i := 1; i <= maxGroupWindowMessages+5; i++ {
		if err := w.Add(20001, testObservation(i), func(string) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	target := first.messages[0].MessageID
	release <- groupWindowDecision{GroupParticipationResponse: coreclient.GroupParticipationResponse{Action: "reply", TargetMessageID: &target}, conversationID: "c1"}
	latest := receiveBatch(t, started)
	if len(latest.messages) != maxGroupWindowMessages || latest.messages[0].MessageID != "m6" || latest.messages[len(latest.messages)-1].MessageID != "m25" {
		t.Fatalf("latest messages = %#v", latest.messages)
	}
	for _, message := range latest.messages {
		if !message.IsNew {
			t.Fatalf("stale decision accepted observation %#v", message)
		}
	}
	release <- groupWindowDecision{GroupParticipationResponse: coreclient.GroupParticipationResponse{Action: "silent"}}
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if replies.Load() != 0 || maximum.Load() != 1 {
		t.Fatalf("replies=%d maximum concurrent decisions=%d", replies.Load(), maximum.Load())
	}
}

type manualTimer struct {
	callback func()
	stopped  atomic.Bool
}

func (t *manualTimer) Stop() bool {
	return !t.stopped.Swap(true)
}

func (t *manualTimer) Fire() { t.callback() }

type scheduledTimer struct {
	delay time.Duration
	timer *manualTimer
}

func TestGroupWindowWaitExpiresAndNewMessageWakesEarly(t *testing.T) {
	batches := make(chan groupWindowBatch, 4)
	decisions := make(chan groupWindowDecision, 4)
	scheduled := make(chan scheduledTimer, 2)
	w, err := newGroupWindow(t.Context(), func(_ context.Context, batch groupWindowBatch) (groupWindowDecision, error) {
		batches <- batch
		return <-decisions, nil
	}, noGroupReply, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.after = func(delay time.Duration, callback func()) stoppableTimer {
		timer := &manualTimer{callback: callback}
		scheduled <- scheduledTimer{delay: delay, timer: timer}
		return timer
	}
	defer w.Close()

	if err := w.Add(20001, testObservation(1), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	receiveBatch(t, batches)
	seven := 7
	decisions <- groupWindowDecision{GroupParticipationResponse: coreclient.GroupParticipationResponse{Action: "wait", WaitSeconds: &seven}}
	firstTimer := receiveTimer(t, scheduled)
	if firstTimer.delay != 7*time.Second {
		t.Fatalf("wait delay = %s", firstTimer.delay)
	}
	if err := w.Add(20001, testObservation(2), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	woken := receiveBatch(t, batches)
	if woken.evaluationReason != "message" || woken.messages[0].IsNew || !woken.messages[1].IsNew || !firstTimer.timer.stopped.Load() {
		t.Fatalf("early wake batch=%#v timer stopped=%v", woken, firstTimer.timer.stopped.Load())
	}
	decisions <- groupWindowDecision{GroupParticipationResponse: coreclient.GroupParticipationResponse{Action: "wait", WaitSeconds: &seven}}
	secondTimer := receiveTimer(t, scheduled)
	firstTimer.timer.Fire()
	select {
	case batch := <-batches:
		t.Fatalf("stale timer started batch %#v", batch)
	case <-time.After(20 * time.Millisecond):
	}
	secondTimer.timer.Fire()
	elapsed := receiveBatch(t, batches)
	if elapsed.evaluationReason != "wait_elapsed" {
		t.Fatalf("reason = %q", elapsed.evaluationReason)
	}
	for _, message := range elapsed.messages {
		if message.IsNew {
			t.Fatalf("wait_elapsed marked message new: %#v", message)
		}
	}
	decisions <- groupWindowDecision{GroupParticipationResponse: coreclient.GroupParticipationResponse{Action: "silent"}}
}

func TestGroupWindowErrorWaitsForAnotherMessageAndCloseCancels(t *testing.T) {
	batches := make(chan groupWindowBatch, 2)
	errorsSeen := make(chan error, 1)
	w, err := newGroupWindow(t.Context(), func(ctx context.Context, batch groupWindowBatch) (groupWindowDecision, error) {
		batches <- batch
		if batch.generation == 1 {
			return groupWindowDecision{}, errors.New("provider failed")
		}
		<-ctx.Done()
		return groupWindowDecision{}, ctx.Err()
	}, noGroupReply, func(_ int64, err error) { errorsSeen <- err })
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add(20001, testObservation(1), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	receiveBatch(t, batches)
	select {
	case err := <-errorsSeen:
		if err.Error() != "provider failed" {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("decision error was not reported")
	}
	select {
	case batch := <-batches:
		t.Fatalf("error retried without message: %#v", batch)
	case <-time.After(20 * time.Millisecond):
	}
	if err := w.Add(20001, testObservation(2), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	receiveBatch(t, batches)
	done := make(chan struct{})
	go func() { w.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the in-flight decision")
	}
}

func noGroupReply(context.Context, groupWindowBatch, groupWindowDecision) error { return nil }

func receiveBatch(t *testing.T, batches <-chan groupWindowBatch) groupWindowBatch {
	t.Helper()
	select {
	case batch := <-batches:
		return batch
	case <-time.After(time.Second):
		t.Fatal("group batch timeout")
		return groupWindowBatch{}
	}
}

func receiveTimer(t *testing.T, timers <-chan scheduledTimer) scheduledTimer {
	t.Helper()
	select {
	case timer := <-timers:
		return timer
	case <-time.After(time.Second):
		t.Fatal("wait timer timeout")
		return scheduledTimer{}
	}
}

func testObservation(i int) coreclient.GroupObservation {
	return coreclient.GroupObservation{
		MessageID: fmt.Sprintf("m%d", i), SenderID: "sender", SenderName: "甲", Text: "消息", TimestampUnixMS: int64(i + 1),
	}
}
