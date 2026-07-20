package companion

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestReplyDeliveryPublishesFirstBeatWithoutPaceWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var completions []BeatReadyCompletion
		var records []replyDeliveryRecord
		delivery := newReplyDelivery(t.Context(), 1, func(completion BeatReadyCompletion) error {
			completions = append(completions, completion)
			return nil
		}, func(record replyDeliveryRecord) {
			records = append(records, record)
		})
		chain := ReplyChain{Text: "第一拍", VisualState: "idle"}
		if err := delivery.Deliver(chain, BeatReadyCompletion{Kind: beatKindFinal, ChainIndex: 0}); err != nil {
			t.Fatalf("Deliver() error = %v", err)
		}
		if len(completions) != 1 || completions[0].TargetIntervalMS != 0 || completions[0].PaceWaitMS != 0 || completions[0].PublishedPrefixCount != 1 {
			t.Fatalf("completions = %#v", completions)
		}
		if !delivery.Complete() || len(delivery.Snapshot()) != 1 {
			t.Fatalf("delivery = complete %v, snapshot %#v", delivery.Complete(), delivery.Snapshot())
		}
		if len(records) != 2 || records[0].Status != "planned" || records[1].Status != "published" {
			t.Fatalf("records = %#v", records)
		}
	})
}

func TestReplyDeliveryPacesSecondBeatAndReportsPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var completions []BeatReadyCompletion
		delivery := newReplyDelivery(t.Context(), 2, func(completion BeatReadyCompletion) error {
			completions = append(completions, completion)
			return nil
		}, nil)
		first := ReplyChain{Text: "第一拍。", VisualState: "idle"}
		second := ReplyChain{Text: "第二拍", VisualState: "happy"}
		if err := delivery.Deliver(first, BeatReadyCompletion{Kind: beatKindFinal, ChainIndex: 0}); err != nil {
			t.Fatalf("Deliver(first) error = %v", err)
		}
		started := time.Now()
		if err := delivery.Deliver(second, BeatReadyCompletion{Kind: beatKindFinal, ChainIndex: 1}); err != nil {
			t.Fatalf("Deliver(second) error = %v", err)
		}
		target := targetReplyInterval(first.Text, second.Text)
		if time.Since(started) != target {
			t.Fatalf("elapsed = %v, want %v", time.Since(started), target)
		}
		if got := completions[1]; got.TargetIntervalMS != target.Milliseconds() || got.PaceWaitMS != target.Milliseconds() || got.PublishedPrefixCount != 2 {
			t.Fatalf("second completion = %#v", got)
		}
	})
}

func TestReplyDeliveryReadyTimeOffsetsPaceWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var completions []BeatReadyCompletion
		delivery := newReplyDelivery(t.Context(), 2, func(completion BeatReadyCompletion) error {
			completions = append(completions, completion)
			return nil
		}, nil)
		first := ReplyChain{Text: "第一拍。", VisualState: "idle"}
		second := ReplyChain{Text: "第二拍", VisualState: "idle"}
		if err := delivery.Deliver(first, BeatReadyCompletion{Kind: beatKindFinal, ChainIndex: 0}); err != nil {
			t.Fatalf("Deliver(first) error = %v", err)
		}
		target := targetReplyInterval(first.Text, second.Text)
		time.Sleep(target + time.Second)
		if err := delivery.Deliver(second, BeatReadyCompletion{Kind: beatKindFinal, ChainIndex: 1}); err != nil {
			t.Fatalf("Deliver(second) error = %v", err)
		}
		if completions[1].PaceWaitMS != 0 {
			t.Fatalf("PaceWaitMS = %d, want 0", completions[1].PaceWaitMS)
		}
	})
}

func TestReplyDeliveryCancellationKeepsPublishedPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var completions []BeatReadyCompletion
		var records []replyDeliveryRecord
		delivery := newReplyDelivery(ctx, 2, func(completion BeatReadyCompletion) error {
			completions = append(completions, completion)
			return nil
		}, func(record replyDeliveryRecord) {
			records = append(records, record)
		})
		first := ReplyChain{Text: "第一拍。", VisualState: "idle"}
		if err := delivery.Deliver(first, BeatReadyCompletion{Kind: beatKindFinal, ChainIndex: 0}); err != nil {
			t.Fatalf("Deliver(first) error = %v", err)
		}
		result := make(chan error, 1)
		go func() {
			result <- delivery.Deliver(ReplyChain{Text: "第二拍", VisualState: "idle"}, BeatReadyCompletion{Kind: beatKindFinal, ChainIndex: 1})
		}()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, ErrTurnInterrupted) {
			t.Fatalf("Deliver(second) error = %v, want ErrTurnInterrupted", err)
		}
		if len(completions) != 1 || len(delivery.Snapshot()) != 1 || delivery.Complete() {
			t.Fatalf("completions = %#v, snapshot = %#v, complete = %v", completions, delivery.Snapshot(), delivery.Complete())
		}
		if got := records[len(records)-1]; got.Status != "cancelled" || got.PublishedPrefixCount != 1 {
			t.Fatalf("last record = %#v", got)
		}
	})
}
