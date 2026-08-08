package contextplan

import (
	"fairy/agent/tool"
	"fairy/runtime/model"
	"math"
	"slices"
)

const maxL1CombinationCandidates = 16

const l1CompactedToolMarkerContent = `{"contextType":"compacted_tool_interaction","status":"result_omitted_after_ttl"}`

type ToolResultInput struct {
	Segments                   []model.ContextSegment
	NowUnixMS                  int64
	RecentProtectedFromOrdinal uint64
	CurrentTokens              uint64
	TargetTokens               uint64
	CacheObservation           model.CachedTokenObservation
	CacheExpired               bool
	ExpectedFutureCalls        uint64
	RefetchCostTokens          uint64
	InformationRiskTokens      uint64
	HardPressure               bool
}

type ToolResultPlan struct {
	CandidateCount         int
	OmittedSegmentIDs      []string
	ReleasedTokens         uint64
	InvalidatedCacheTokens uint64
	AfterTokens            uint64
	NetBenefitTokens       int64
}

type l1Candidate struct {
	result model.ContextSegment
	pair   model.ContextSegment
}

func PlanToolResults(input ToolResultInput) ToolResultPlan {
	candidates := eligibleL1Candidates(input)
	result := ToolResultPlan{
		CandidateCount: len(candidates),
		AfterTokens:    input.CurrentTokens,
	}
	if len(candidates) == 0 {
		return result
	}
	if len(candidates) > maxL1CombinationCandidates {
		candidates = candidates[len(candidates)-maxL1CombinationCandidates:]
		result.CandidateCount = len(candidates)
	}
	expectedCalls := input.ExpectedFutureCalls
	if expectedCalls == 0 {
		expectedCalls = 1
	}
	var best ToolResultPlan
	best.AfterTokens = input.CurrentTokens
	bestScore := int64(math.MinInt64)
	for mask := 1; mask < 1<<len(candidates); mask++ {
		selected := make([]l1Candidate, 0, len(candidates))
		for index, candidate := range candidates {
			if mask&(1<<index) != 0 {
				selected = append(selected, candidate)
			}
		}
		plan := scoreL1CandidateSet(input, selected, expectedCalls)
		if !input.HardPressure && plan.NetBenefitTokens <= 0 {
			continue
		}
		reachesTarget := plan.AfterTokens <= input.TargetTokens
		bestReachesTarget := best.OmittedSegmentIDs != nil && best.AfterTokens <= input.TargetTokens
		score := plan.NetBenefitTokens
		if reachesTarget {
			score += math.MaxInt32
		}
		if bestReachesTarget && !reachesTarget {
			continue
		}
		if score > bestScore ||
			(score == bestScore && plan.ReleasedTokens < best.ReleasedTokens) {
			best = plan
			bestScore = score
		}
	}
	if best.OmittedSegmentIDs == nil {
		return result
	}
	best.CandidateCount = result.CandidateCount
	return best
}

func eligibleL1Candidates(input ToolResultInput) []l1Candidate {
	byID := make(map[string]model.ContextSegment, len(input.Segments))
	dependents := make(map[string]int)
	for _, segment := range input.Segments {
		byID[segment.ID] = segment
		if segment.ProjectionState != model.ContextProjectionActive {
			continue
		}
		for _, dependency := range segment.Dependencies {
			dependents[dependency]++
		}
	}
	candidates := make([]l1Candidate, 0)
	for _, segment := range input.Segments {
		if segment.Kind != model.ContextSegmentToolResult ||
			segment.ProjectionState != model.ContextProjectionActive ||
			segment.RetentionPolicy != model.ContextRetentionTTL ||
			segment.ExpiresAtUnixMS == nil ||
			*segment.ExpiresAtUnixMS > input.NowUnixMS ||
			segment.Recoverability != model.ContextRecoverabilityRefetchable ||
			(input.RecentProtectedFromOrdinal > 0 && segment.Ordinal >= input.RecentProtectedFromOrdinal) ||
			len(segment.Dependencies) != 1 {
			continue
		}
		pair, exists := byID[segment.Dependencies[0]]
		if !exists || pair.Kind != model.ContextSegmentToolCall ||
			pair.ProjectionState != model.ContextProjectionActive ||
			dependents[pair.ID] != 1 {
			continue
		}
		if dependents[segment.ID] != 0 {
			continue
		}
		candidates = append(candidates, l1Candidate{result: segment, pair: pair})
	}
	return candidates
}

func scoreL1CandidateSet(input ToolResultInput, selected []l1Candidate, expectedCalls uint64) ToolResultPlan {
	ids := make([]string, 0, len(selected)*2)
	seen := make(map[string]struct{}, len(selected)*2)
	earliest := uint64(math.MaxUint64)
	var released uint64
	for _, candidate := range selected {
		for _, segment := range []model.ContextSegment{candidate.pair, candidate.result} {
			if _, exists := seen[segment.ID]; exists {
				continue
			}
			seen[segment.ID] = struct{}{}
			ids = append(ids, segment.ID)
			released += segment.TokenCount
			if segment.Ordinal < earliest {
				earliest = segment.Ordinal
			}
		}
	}
	markerTokens := tool.PromptItemTokenCount(l1CompactedToolMarker()) * uint64(len(selected))
	if released > markerTokens {
		released -= markerTokens
	} else {
		released = 0
	}
	var suffixTokens uint64
	for _, segment := range input.Segments {
		if segment.ProjectionState == model.ContextProjectionActive && segment.Ordinal >= earliest {
			suffixTokens += segment.TokenCount
		}
	}
	invalidated := uint64(0)
	if !input.CacheExpired {
		switch input.CacheObservation.Status {
		case "observed":
			if input.CacheObservation.Tokens != nil {
				invalidated = min(suffixTokens, *input.CacheObservation.Tokens)
			}
		case "missing", "unsupported":
			invalidated = suffixTokens
		default:
			invalidated = suffixTokens
		}
	}
	cost := invalidated + input.RefetchCostTokens*uint64(len(selected)) +
		input.InformationRiskTokens*uint64(len(selected))
	benefit := released * expectedCalls
	net := signedTokenDifference(benefit, cost)
	after := uint64(0)
	if input.CurrentTokens > released {
		after = input.CurrentTokens - released
	}
	slices.Sort(ids)
	return ToolResultPlan{
		OmittedSegmentIDs: ids, ReleasedTokens: released,
		InvalidatedCacheTokens: invalidated, AfterTokens: after,
		NetBenefitTokens: net,
	}
}

func signedTokenDifference(left, right uint64) int64 {
	if left >= right {
		difference := left - right
		if difference > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(difference)
	}
	difference := right - left
	if difference > math.MaxInt64 {
		return math.MinInt64
	}
	return -int64(difference)
}

func ApplyToolResultPlan(segments []model.ContextSegment, plan ToolResultPlan) []model.ContextSegment {
	if len(plan.OmittedSegmentIDs) == 0 {
		return append([]model.ContextSegment(nil), segments...)
	}
	omitted := make(map[string]struct{}, len(plan.OmittedSegmentIDs))
	for _, id := range plan.OmittedSegmentIDs {
		omitted[id] = struct{}{}
	}
	projected := append([]model.ContextSegment(nil), segments...)
	for index := range projected {
		if _, exists := omitted[projected[index].ID]; !exists {
			continue
		}
		switch projected[index].Kind {
		case model.ContextSegmentToolCall:
			marker := l1CompactedToolMarker()
			projected[index].Kind = model.ContextSegmentContextData
			projected[index].Item = &marker
			projected[index].TokenCount = tool.PromptItemTokenCount(marker)
			projected[index].Recoverability = model.ContextRecoverabilityRequired
			projected[index].ProjectionState = model.ContextProjectionActive
		case model.ContextSegmentToolResult:
			projected[index].ProjectionState = model.ContextProjectionOmittedL1
		}
	}
	return projected
}

func l1CompactedToolMarker() model.PromptItem {
	return model.PromptItem{
		Type:    model.PromptItemContextData,
		Content: l1CompactedToolMarkerContent,
	}
}
