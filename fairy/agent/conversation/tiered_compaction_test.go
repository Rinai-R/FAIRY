package conversation

import (
	"testing"

	"fairy/agent/conversation/contextplan"
	history "fairy/context/history/transcript"
)

func TestCompactionWatermarksAreOrderedAndClassifyPressure(t *testing.T) {
	policy := contextplan.PolicyFromContextWindow(100_000)
	if policy.ShouldCompact(contextplan.TriggerAfterCompletedTurn, policy.SoftInputTokens-1, true) {
		t.Fatal("below soft watermark should not compact")
	}
	if !policy.ShouldCompact(contextplan.TriggerAfterCompletedTurn, policy.SoftInputTokens, true) {
		t.Fatal("soft watermark should start tiered compaction")
	}
	if policy.HardPressure(policy.HardInputTokens - 1) {
		t.Fatal("below hard watermark classified as hard")
	}
	if !policy.HardPressure(policy.HardInputTokens) {
		t.Fatal("hard watermark did not classify as hard")
	}
}

func TestRecentTailStartSequenceKeepsCompleteTurns(t *testing.T) {
	messages := []history.MessageRecord{
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
	plan := contextplan.ToolResultPlan{OmittedSegmentIDs: []string{"tool:call", "tool:result"}, AfterTokens: 240}
	soft := uint64(275)
	policy := contextplan.Policy{
		AutoInputTokenThreshold: &soft,
		TargetInputTokens:       250,
		SoftInputTokens:         soft,
		HardInputTokens:         290,
	}
	if policy.ShouldCompactAfterTurn(plan.AfterTokens) {
		t.Fatalf("follow-on compaction scheduled after L1 target recovery: %#v", plan)
	}
}
