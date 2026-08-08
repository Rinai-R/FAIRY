package contextplan

import (
	"testing"

	"fairy/agent/tool"
	"fairy/runtime/model"
)

func TestPlanL1RejectsExpiredNegativeROICandidateAtSoftPressure(t *testing.T) {
	segments := l1PlannerFixture()
	cached := uint64(200)
	plan := PlanToolResults(ToolResultInput{
		Segments: segments, NowUnixMS: 100, CurrentTokens: 300, TargetTokens: 200,
		CacheObservation: model.CacheObserved(cached), ExpectedFutureCalls: 1,
		RefetchCostTokens: 10, InformationRiskTokens: 10,
	})
	if plan.CandidateCount != 2 || len(plan.OmittedSegmentIDs) != 0 || plan.AfterTokens != 300 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlanL1SelectsBestSetUsingEarliestChangedSuffix(t *testing.T) {
	segments := l1PlannerFixture()
	plan := PlanToolResults(ToolResultInput{
		Segments: segments, NowUnixMS: 100, CurrentTokens: 300, TargetTokens: 210,
		CacheExpired: true, ExpectedFutureCalls: 2, RefetchCostTokens: 140,
	})
	if len(plan.OmittedSegmentIDs) != 2 ||
		plan.OmittedSegmentIDs[0] != "tool:late:call" ||
		plan.OmittedSegmentIDs[1] != "tool:late:result" ||
		plan.ReleasedTokens != 100-tool.PromptItemTokenCount(l1CompactedToolMarker()) ||
		plan.InvalidatedCacheTokens != 0 ||
		plan.AfterTokens != 200+tool.PromptItemTokenCount(l1CompactedToolMarker()) {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlanL1MissingCacheObservationUsesConservativeSuffixCost(t *testing.T) {
	segments := l1PlannerFixture()
	plan := PlanToolResults(ToolResultInput{
		Segments: segments, NowUnixMS: 100, CurrentTokens: 300, TargetTokens: 250,
		CacheObservation: model.CacheMissing(), ExpectedFutureCalls: 1,
	})
	if len(plan.OmittedSegmentIDs) != 0 {
		t.Fatalf("plan = %#v", plan)
	}
	hard := PlanToolResults(ToolResultInput{
		Segments: segments, NowUnixMS: 100, CurrentTokens: 300, TargetTokens: 250,
		CacheObservation: model.CacheMissing(), ExpectedFutureCalls: 1, HardPressure: true,
	})
	if len(hard.OmittedSegmentIDs) == 0 || hard.InvalidatedCacheTokens == 0 {
		t.Fatalf("hard plan = %#v", hard)
	}
}

func TestPlanL1FiltersProtectedDependentAndEphemeralResults(t *testing.T) {
	segments := l1PlannerFixture()
	segments[1].Recoverability = model.ContextRecoverabilityEphemeral
	segments[3].Dependencies = append(segments[3].Dependencies, "unknown")
	plan := PlanToolResults(ToolResultInput{
		Segments: segments, NowUnixMS: 100, CurrentTokens: 300, TargetTokens: 200,
		CacheExpired: true, RecentProtectedFromOrdinal: 3,
	})
	if plan.CandidateCount != 0 || len(plan.OmittedSegmentIDs) != 0 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestApplyL1CompactionPlanReplacesPairWithContentFreeMarker(t *testing.T) {
	segments := l1PlannerFixture()
	projected := ApplyToolResultPlan(segments, ToolResultPlan{
		OmittedSegmentIDs: []string{"tool:late:call", "tool:late:result"},
	})
	if projected[2].ProjectionState != model.ContextProjectionActive ||
		projected[2].Kind != model.ContextSegmentContextData ||
		projected[2].Item == nil ||
		projected[2].Item.Type != model.PromptItemContextData ||
		projected[2].Item.Content != l1CompactedToolMarkerContent ||
		projected[3].ProjectionState != model.ContextProjectionOmittedL1 {
		t.Fatalf("projected = %#v", projected)
	}
	if segments[2].ProjectionState != model.ContextProjectionActive {
		t.Fatal("input segments were mutated")
	}
}

func l1PlannerFixture() []model.ContextSegment {
	expired := int64(50)
	return []model.ContextSegment{
		{
			ID: "tool:early:call", Ordinal: 1, Kind: model.ContextSegmentToolCall,
			Item:       &model.PromptItem{Type: model.PromptItemToolCall, ToolCallID: "early", ToolName: "search"},
			TokenCount: 5, RetentionPolicy: model.ContextRetentionCurrentTurn,
			Recoverability: model.ContextRecoverabilityRequired, ProjectionState: model.ContextProjectionActive,
		},
		{
			ID: "tool:early:result", Ordinal: 2, Kind: model.ContextSegmentToolResult,
			Item:            &model.PromptItem{Type: model.PromptItemToolResult, ToolCallID: "early", Content: "large"},
			ExpiresAtUnixMS: &expired, TokenCount: 60, RetentionPolicy: model.ContextRetentionTTL,
			Recoverability: model.ContextRecoverabilityRefetchable, Dependencies: []string{"tool:early:call"},
			ProjectionState: model.ContextProjectionActive,
		},
		{
			ID: "tool:late:call", Ordinal: 3, Kind: model.ContextSegmentToolCall,
			Item:       &model.PromptItem{Type: model.PromptItemToolCall, ToolCallID: "late", ToolName: "search"},
			TokenCount: 5, RetentionPolicy: model.ContextRetentionCurrentTurn,
			Recoverability: model.ContextRecoverabilityRequired, ProjectionState: model.ContextProjectionActive,
		},
		{
			ID: "tool:late:result", Ordinal: 4, Kind: model.ContextSegmentToolResult,
			Item:            &model.PromptItem{Type: model.PromptItemToolResult, ToolCallID: "late", Content: "larger"},
			ExpiresAtUnixMS: &expired, TokenCount: 95, RetentionPolicy: model.ContextRetentionTTL,
			Recoverability: model.ContextRecoverabilityRefetchable, Dependencies: []string{"tool:late:call"},
			ProjectionState: model.ContextProjectionActive,
		},
		{
			ID: "tail", Ordinal: 5, Kind: model.ContextSegmentContextData,
			Item:       &model.PromptItem{Type: model.PromptItemContextData, Content: "tail"},
			TokenCount: 100, RetentionPolicy: model.ContextRetentionRecent,
			Recoverability: model.ContextRecoverabilityRequired, ProjectionState: model.ContextProjectionActive,
		},
	}
}
