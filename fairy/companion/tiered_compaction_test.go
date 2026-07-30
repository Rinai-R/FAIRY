package companion

import (
	"testing"

	"fairy/memory"
)

func TestCompactionWatermarksAreOrderedAndClassifyPressure(t *testing.T) {
	policy := compactionPolicyFromContextWindow(100_000)
	if policy.shouldCompact(compactionTriggerAfterCompletedTurn, policy.SoftInputTokens-1, true) {
		t.Fatal("below soft watermark should not compact")
	}
	if !policy.shouldCompact(compactionTriggerAfterCompletedTurn, policy.SoftInputTokens, true) {
		t.Fatal("soft watermark should start tiered compaction")
	}
	if policy.hardPressure(policy.HardInputTokens - 1) {
		t.Fatal("below hard watermark classified as hard")
	}
	if !policy.hardPressure(policy.HardInputTokens) {
		t.Fatal("hard watermark did not classify as hard")
	}
}

func TestRecentTailStartSequenceKeepsCompleteTurns(t *testing.T) {
	messages := []memory.MessageRecord{
		{TurnID: "turn-1", Sequence: 1, Role: "user"},
		{TurnID: "turn-1", Sequence: 2, Role: "assistant"},
		{TurnID: "turn-2", Sequence: 3, Role: "user"},
		{TurnID: "turn-2", Sequence: 4, Role: "assistant"},
		{TurnID: "turn-3", Sequence: 5, Role: "user"},
		{TurnID: "turn-3", Sequence: 6, Role: "assistant"},
	}
	if got := recentTailStartSequence(messages, 2); got != 3 {
		t.Fatalf("recentTailStartSequence() = %d, want 3", got)
	}
}

func TestL1TargetRecoveryPreventsFollowOnCompaction(t *testing.T) {
	plan := planL1ToolResultCompaction(l1PlanningInput{
		Segments: l1PlannerFixture(), NowUnixMS: 100,
		CurrentTokens: 300, TargetTokens: 250,
		CacheExpired: true, ExpectedFutureCalls: 2,
	})
	if len(plan.OmittedSegmentIDs) == 0 || plan.AfterTokens > 250 {
		t.Fatalf("L1 plan did not recover target: %#v", plan)
	}
	soft := uint64(275)
	policy := compactionPolicy{
		AutoInputTokenThreshold: &soft,
		TargetInputTokens:       250,
		SoftInputTokens:         soft,
		HardInputTokens:         290,
	}
	if policy.shouldCompactAfterTurn(plan.AfterTokens) {
		t.Fatalf("follow-on compaction scheduled after L1 target recovery: %#v", plan)
	}
}
