package contextplan

import "testing"

func TestPolicyFromContextWindowReservesOutput(t *testing.T) {
	policy := PolicyFromContextWindow(10_000)
	if policy.AutoInputTokenThreshold == nil {
		t.Fatal("expected threshold")
	}
	want := uint64(10_000*SoftWatermarkBasisPoints/10_000 - RespondOutputReserveTokens)
	if *policy.AutoInputTokenThreshold != want {
		t.Fatalf("threshold = %d, want %d", *policy.AutoInputTokenThreshold, want)
	}
	if !(policy.TargetInputTokens < policy.SoftInputTokens && policy.SoftInputTokens < policy.HardInputTokens) {
		t.Fatalf("watermarks = target:%d soft:%d hard:%d", policy.TargetInputTokens, policy.SoftInputTokens, policy.HardInputTokens)
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
