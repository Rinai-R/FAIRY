package companion

import "testing"

func TestPolicyFromContextWindowReservesOutput(t *testing.T) {
	policy := compactionPolicyFromContextWindow(10_000)
	if policy.AutoInputTokenThreshold == nil {
		t.Fatal("expected threshold")
	}
	want := uint64(10_000*softWatermarkBasisPoints/10_000 - respondOutputReserveTokens)
	if *policy.AutoInputTokenThreshold != want {
		t.Fatalf("threshold = %d, want %d", *policy.AutoInputTokenThreshold, want)
	}
	if !(policy.TargetInputTokens < policy.SoftInputTokens && policy.SoftInputTokens < policy.HardInputTokens) {
		t.Fatalf("watermarks = target:%d soft:%d hard:%d", policy.TargetInputTokens, policy.SoftInputTokens, policy.HardInputTokens)
	}
}

func TestShouldCompactAfterTurnRequiresKnownUsage(t *testing.T) {
	threshold := uint64(100)
	policy := compactionPolicy{AutoInputTokenThreshold: &threshold}
	if policy.shouldCompact(compactionTriggerAfterCompletedTurn, 100, false) {
		t.Fatal("unknown usage should not compact")
	}
	if !policy.shouldCompact(compactionTriggerAfterCompletedTurn, 100, true) {
		t.Fatal("known usage at threshold should compact")
	}
}
