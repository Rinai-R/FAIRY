package compaction

import "testing"

func TestPolicyFromContextWindowReservesOutput(t *testing.T) {
	policy := PolicyFromContextWindow(10_000)
	if policy.AutoInputTokenThreshold == nil {
		t.Fatal("expected threshold")
	}
	want := uint64(10_000*8_000/10_000 - RespondOutputReserveTokens)
	if *policy.AutoInputTokenThreshold != want {
		t.Fatalf("threshold = %d, want %d", *policy.AutoInputTokenThreshold, want)
	}
}

func TestShouldCompactAfterTurnRequiresKnownUsage(t *testing.T) {
	threshold := uint64(100)
	policy := Policy{AutoInputTokenThreshold: &threshold}
	if policy.ShouldCompact(TriggerAfterCompletedTurn, 100, false) {
		t.Fatal("unknown usage should not compact")
	}
	if !policy.ShouldCompact(TriggerAfterCompletedTurn, 100, true) {
		t.Fatal("known usage at threshold should compact")
	}
}
