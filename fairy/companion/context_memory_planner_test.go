package companion

import (
	"testing"

	"fairy/memory"
	"fairy/model"
)

func TestPlanL2RequiresPersonalPolicyAndCommittedCoverage(t *testing.T) {
	coverage := []memory.MemoryContextCoverage{{
		ConversationID: "conversation", TurnID: "turn-1", MemoryID: "memory-1",
		StartMessageSequence: 1, EndMessageSequence: 2, CoveredTokens: 80,
	}}
	public := planL2MemoryCompaction(l2PlanningInput{
		Coverage: coverage, AllowsPersonalMemory: false,
		CurrentTokens: 200, TargetTokens: 100, CacheExpired: true,
	})
	if public.CandidateCount != 0 || len(public.Omissions) != 0 {
		t.Fatalf("public plan = %#v", public)
	}
	pendingOrClaimed := planL2MemoryCompaction(l2PlanningInput{
		AllowsPersonalMemory: true, CurrentTokens: 200, TargetTokens: 100,
		CacheExpired: true, HardPressure: true,
	})
	if pendingOrClaimed.CandidateCount != 0 || len(pendingOrClaimed.Omissions) != 0 {
		t.Fatalf("uncommitted extraction plan = %#v", pendingOrClaimed)
	}
	personal := planL2MemoryCompaction(l2PlanningInput{
		Coverage: coverage, AllowsPersonalMemory: true,
		CurrentTokens: 200, TargetTokens: 120, CacheExpired: true,
	})
	if len(personal.Omissions) != 1 || personal.Omissions[0].MemoryID != "memory-1" {
		t.Fatalf("personal plan = %#v", personal)
	}
}

func TestPlanL2ProtectsRecentTailAndRejectsMissingCacheAtSoftPressure(t *testing.T) {
	coverage := []memory.MemoryContextCoverage{
		{MemoryID: "old", StartMessageSequence: 1, EndMessageSequence: 2, CoveredTokens: 80},
		{MemoryID: "recent", StartMessageSequence: 3, EndMessageSequence: 4, CoveredTokens: 80},
	}
	soft := planL2MemoryCompaction(l2PlanningInput{
		Coverage: coverage, AllowsPersonalMemory: true, RecentTailStartSequence: 3,
		CurrentTokens: 200, TargetTokens: 120, CacheObservation: model.CacheMissing(),
	})
	if soft.CandidateCount != 1 || len(soft.Omissions) != 0 {
		t.Fatalf("soft plan = %#v", soft)
	}
	hard := planL2MemoryCompaction(l2PlanningInput{
		Coverage: coverage, AllowsPersonalMemory: true, RecentTailStartSequence: 3,
		CurrentTokens: 200, TargetTokens: 120, CacheObservation: model.CacheMissing(),
		HardPressure: true,
	})
	if len(hard.Omissions) != 1 || hard.Omissions[0].MemoryID != "old" {
		t.Fatalf("hard plan = %#v", hard)
	}
}

func TestPlanL2SkipsAlreadyProjectedCoverage(t *testing.T) {
	plan := planL2MemoryCompaction(l2PlanningInput{
		Coverage: []memory.MemoryContextCoverage{{
			MemoryID: "memory-1", StartMessageSequence: 1, EndMessageSequence: 2, CoveredTokens: 80,
		}},
		ExistingProjection: memory.PromptProjectionState{
			Version: memory.PromptProjectionVersion,
			Omissions: []memory.PromptProjectionOmission{{
				StartMessageSequence: 1, EndMessageSequence: 2,
				Reason: "memory_committed", MemoryID: "memory-1",
			}},
		},
		AllowsPersonalMemory: true, CurrentTokens: 200, TargetTokens: 120, CacheExpired: true,
	})
	if plan.CandidateCount != 0 || len(plan.Omissions) != 0 {
		t.Fatalf("plan = %#v", plan)
	}
}
