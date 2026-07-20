package companion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestTargetReplyInterval(t *testing.T) {
	tests := []struct {
		name     string
		previous string
		current  string
		want     time.Duration
	}{
		{name: "minimum for short CJK", current: "你好", want: replyPaceMinimum},
		{name: "minimum for short ASCII", current: "hello", want: replyPaceMinimum},
		{name: "CJK weighted", current: strings.Repeat("好", 20), want: 1620 * time.Millisecond},
		{name: "mixed scripts", current: "好a", want: replyPaceMinimum},
		{name: "strong punctuation pause", previous: "好。", current: strings.Repeat("a", 10), want: 790 * time.Millisecond},
		{name: "weak punctuation pause", previous: "好，", current: strings.Repeat("a", 10), want: 670 * time.Millisecond},
		{name: "trailing space punctuation", previous: "好？  ", current: strings.Repeat("a", 10), want: 790 * time.Millisecond},
		{name: "maximum clamp", current: strings.Repeat("好", 100), want: replyPaceMaximum},
		{name: "whitespace ignored", current: "   \n\t", want: replyPaceMinimum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := targetReplyInterval(tt.previous, tt.current); got != tt.want {
				t.Fatalf("targetReplyInterval(%q, %q) = %v, want %v", tt.previous, tt.current, got, tt.want)
			}
		})
	}
}

func TestReplyPacerFirstBeatHasNoWait(t *testing.T) {
	var pacer replyPacer
	target, waited, err := pacer.Wait(t.Context(), "第一拍")
	if err != nil || target != 0 || waited != 0 {
		t.Fatalf("Wait() = (%v, %v, %v), want zeros", target, waited, err)
	}
}

func TestReplyPacerWaitsOnlyRemainingBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var pacer replyPacer
		pacer.Published("第一拍。")
		time.Sleep(300 * time.Millisecond)

		started := time.Now()
		target, waited, err := pacer.Wait(t.Context(), "第二拍")
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
		wantTarget := targetReplyInterval("第一拍。", "第二拍")
		if target != wantTarget {
			t.Fatalf("target = %v, want %v", target, wantTarget)
		}
		wantWait := wantTarget - 300*time.Millisecond
		if waited != wantWait || time.Since(started) != wantWait {
			t.Fatalf("waited = %v, elapsed = %v, want %v", waited, time.Since(started), wantWait)
		}
	})
}

func TestReplyPacerReadyAfterBudgetDoesNotWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var pacer replyPacer
		pacer.Published("第一拍。")
		target := targetReplyInterval("第一拍。", "第二拍")
		time.Sleep(target + time.Second)

		started := time.Now()
		gotTarget, waited, err := pacer.Wait(t.Context(), "第二拍")
		if err != nil || gotTarget != target || waited != 0 || time.Since(started) != 0 {
			t.Fatalf("Wait() = (%v, %v, %v), elapsed %v", gotTarget, waited, err, time.Since(started))
		}
	})
}

func TestReplyPacerCancellationStopsWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var pacer replyPacer
		pacer.Published("第一拍。")
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			_, _, err := pacer.Wait(ctx, "第二拍")
			result <- err
		}()

		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait() error = %v, want context.Canceled", err)
		}
	})
}
