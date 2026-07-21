package companion

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestAmbientInboxStartsFirstMessageImmediately(t *testing.T) {
	service := NewCompanionService()
	defer service.Close()
	started := make(chan ambientBatch, 1)
	service.ambient.decideHook = func(_ context.Context, batch ambientBatch) (GroupParticipationResult, error) {
		started <- batch
		return GroupParticipationResult{Action: GroupParticipationSilent}, nil
	}
	if err := service.ObserveGroupMessage("c1", GroupObservation{
		MessageID: "m1", SenderID: "u1", SenderName: "n", Text: "hi", TimestampUnixMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-started:
		if batch.evaluationReason != GroupParticipationReasonMessage || len(batch.messages) != 1 || !batch.messages[0].IsNew {
			t.Fatalf("batch = %#v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("first message did not start participation immediately")
	}
}

func TestAmbientInboxSerializesAndKeepsLatestTwenty(t *testing.T) {
	service := NewCompanionService()
	defer service.Close()
	started := make(chan ambientBatch, 2)
	release := make(chan GroupParticipationResult, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	service.ambient.decideHook = func(_ context.Context, batch ambientBatch) (GroupParticipationResult, error) {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		started <- batch
		decision := <-release
		active.Add(-1)
		return decision, nil
	}
	if err := service.ObserveGroupMessage("c1", testAmbientObservation(0)); err != nil {
		t.Fatal(err)
	}
	first := receiveAmbientBatch(t, started)
	for i := 1; i <= maxGroupObservations+5; i++ {
		if err := service.ObserveGroupMessage("c1", testAmbientObservation(i)); err != nil {
			t.Fatal(err)
		}
	}
	target := first.messages[0].MessageID
	release <- GroupParticipationResult{Action: GroupParticipationReply, TargetMessageID: &target}
	latest := receiveAmbientBatch(t, started)
	if len(latest.messages) != maxGroupObservations || latest.messages[0].MessageID != "m6" || latest.messages[len(latest.messages)-1].MessageID != "m25" {
		t.Fatalf("latest messages = %#v", latest.messages)
	}
	release <- GroupParticipationResult{Action: GroupParticipationSilent}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if maximum.Load() <= 1 && active.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maximum.Load() > 1 {
		t.Fatalf("concurrent participation runs = %d", maximum.Load())
	}
}

func receiveAmbientBatch(t *testing.T, ch <-chan ambientBatch) ambientBatch {
	t.Helper()
	select {
	case batch := <-ch:
		return batch
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ambient batch")
		return ambientBatch{}
	}
}

func testAmbientObservation(i int) GroupObservation {
	return GroupObservation{
		MessageID: fmt.Sprintf("m%d", i), SenderID: "u1", SenderName: "n", Text: "t", TimestampUnixMS: int64(i + 1),
	}
}
