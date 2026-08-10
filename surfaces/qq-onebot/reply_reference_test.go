package main

import (
	"fmt"
	"testing"
	"time"
)

func TestReplyDistanceTrackerUsesBoundedPositionAndTimePolicy(t *testing.T) {
	base := time.Unix(100, 0)
	tracker := &replyDistanceTracker{}
	tracker.Observe("target", base)
	if tracker.ShouldQuote("target", base.Add(replyElapsedThreshold-time.Second), 2) {
		t.Fatal("near target was quoted before threshold")
	}
	tracker.Observe("later-1", base.Add(time.Second))
	if tracker.ShouldQuote("target", base.Add(2*time.Second), 2) {
		t.Fatal("one later message exceeded the message gap")
	}
	tracker.Observe("later-2", base.Add(2*time.Second))
	if !tracker.ShouldQuote("target", base.Add(3*time.Second), 2) {
		t.Fatal("two later messages did not trigger quote")
	}

	timed := &replyDistanceTracker{}
	timed.Observe("slow-target", base)
	if !timed.ShouldQuote("slow-target", base.Add(replyElapsedThreshold), replyMessageGapMax) {
		t.Fatal("elapsed threshold did not trigger quote")
	}

	maximumGap := &replyDistanceTracker{}
	maximumGap.Observe("maximum-target", base)
	for index := uint64(1); index < replyMessageGapMax; index++ {
		maximumGap.Observe(fmt.Sprintf("maximum-later-%d", index), base.Add(time.Duration(index)*time.Second))
	}
	if maximumGap.ShouldQuote("maximum-target", base.Add(5*time.Second), replyMessageGapMax) {
		t.Fatal("maximum gap triggered before five later messages")
	}
	maximumGap.Observe("maximum-later-5", base.Add(5*time.Second))
	if !maximumGap.ShouldQuote("maximum-target", base.Add(6*time.Second), replyMessageGapMax) {
		t.Fatal("maximum gap did not trigger after five later messages")
	}
}

func TestReplyDistanceTrackerBoundsAndDeduplicatesMessageIDs(t *testing.T) {
	base := time.Unix(100, 0)
	tracker := &replyDistanceTracker{}
	tracker.Observe("duplicate", base)
	tracker.Observe("duplicate", base.Add(time.Second))
	if tracker.sequence != 1 || len(tracker.entries) != 1 {
		t.Fatalf("duplicate changed position: sequence=%d entries=%d", tracker.sequence, len(tracker.entries))
	}
	for index := 0; index < replyPositionLimit; index++ {
		tracker.Observe(fmt.Sprintf("message-%d", index), base.Add(time.Duration(index+2)*time.Second))
	}
	if len(tracker.entries) != replyPositionLimit || len(tracker.byID) != replyPositionLimit {
		t.Fatalf("tracker size = %d/%d, want %d", len(tracker.entries), len(tracker.byID), replyPositionLimit)
	}
	if tracker.ShouldQuote("duplicate", base.Add(time.Hour), replyMessageGapMin) {
		t.Fatal("evicted target remained available")
	}
}

func TestTurnReplyClaimsConsumeOnceAndReleaseAtTerminal(t *testing.T) {
	samples := []uint64{replyMessageGapMin, replyMessageGapMax, 3}
	sampleIndex := 0
	claims := newTurnReplyClaimsWithSampler(func() uint64 {
		value := samples[sampleIndex]
		sampleIndex++
		return value
	})
	gap, claimed := claims.Claim("turn-1")
	if !claimed || gap != replyMessageGapMin {
		t.Fatalf("first turn gap = %d, claimed=%v", gap, claimed)
	}
	if _, claimed = claims.Claim("turn-1"); claimed || sampleIndex != 1 {
		t.Fatal("turn anchor was not consumed exactly once")
	}
	gap, claimed = claims.Claim("turn-2")
	if !claimed || gap != replyMessageGapMax || sampleIndex != 2 {
		t.Fatalf("second turn gap = %d, claimed=%v, samples=%d", gap, claimed, sampleIndex)
	}
	claims.Release("turn-1")
	if gap, claimed = claims.Claim("turn-1"); !claimed || gap != 3 || sampleIndex != 3 {
		t.Fatal("terminal release did not clear turn anchor")
	}
	for _, state := range []string{"completed", "failed", "interrupted"} {
		if !terminalTurnState(state) {
			t.Fatalf("terminal state %q was not recognized", state)
		}
	}
	if terminalTurnState("responding") {
		t.Fatal("responding was treated as terminal")
	}
}

func TestReplyMessageIDValidationRejectsUnsafeIdentity(t *testing.T) {
	for _, value := range []string{"", " target ", "target\n1", string([]byte{0xff})} {
		if validReplyMessageID(value) {
			t.Fatalf("unsafe reply message ID accepted: %q", value)
		}
	}
	if !validReplyMessageID("guild-message-7") {
		t.Fatal("valid reply message ID rejected")
	}
}
