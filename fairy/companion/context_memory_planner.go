package companion

import (
	"slices"

	"fairy/memory"
	"fairy/model"
)

type l2PlanningInput struct {
	Coverage                []memory.MemoryContextCoverage
	ExistingProjection      memory.PromptProjectionState
	AllowsPersonalMemory    bool
	RecentTailStartSequence uint64
	CurrentTokens           uint64
	TargetTokens            uint64
	CacheObservation        model.CachedTokenObservation
	CacheExpired            bool
	ExpectedFutureCalls     uint64
	HardPressure            bool
}

type l2CompactionPlan struct {
	CandidateCount         int
	Omissions              []memory.PromptProjectionOmission
	ReleasedTokens         uint64
	InvalidatedCacheTokens uint64
	AfterTokens            uint64
	NetBenefitTokens       int64
}

func planL2MemoryCompaction(input l2PlanningInput) l2CompactionPlan {
	result := l2CompactionPlan{AfterTokens: input.CurrentTokens}
	if !input.AllowsPersonalMemory {
		return result
	}
	existing := make(map[uint64]struct{})
	for _, omission := range input.ExistingProjection.Omissions {
		for sequence := omission.StartMessageSequence; sequence > 0 && sequence <= omission.EndMessageSequence; sequence++ {
			existing[sequence] = struct{}{}
		}
	}
	candidates := make([]memory.MemoryContextCoverage, 0, len(input.Coverage))
	for _, coverage := range input.Coverage {
		if coverage.StartMessageSequence == 0 ||
			coverage.EndMessageSequence < coverage.StartMessageSequence ||
			coverage.CoveredTokens == 0 ||
			(input.RecentTailStartSequence > 0 && coverage.EndMessageSequence >= input.RecentTailStartSequence) {
			continue
		}
		if _, omitted := existing[coverage.StartMessageSequence]; omitted {
			continue
		}
		candidates = append(candidates, coverage)
	}
	result.CandidateCount = len(candidates)
	if len(candidates) == 0 {
		return result
	}
	slices.SortFunc(candidates, func(left, right memory.MemoryContextCoverage) int {
		switch {
		case left.StartMessageSequence < right.StartMessageSequence:
			return -1
		case left.StartMessageSequence > right.StartMessageSequence:
			return 1
		default:
			return 0
		}
	})
	expectedCalls := input.ExpectedFutureCalls
	if expectedCalls == 0 {
		expectedCalls = 1
	}
	var released uint64
	for _, candidate := range candidates {
		result.Omissions = append(result.Omissions, memory.PromptProjectionOmission{
			StartMessageSequence: candidate.StartMessageSequence,
			EndMessageSequence:   candidate.EndMessageSequence,
			Reason:               "memory_committed",
			MemoryID:             candidate.MemoryID,
		})
		released += candidate.CoveredTokens
		if input.CurrentTokens <= released ||
			input.CurrentTokens-released <= input.TargetTokens {
			break
		}
	}
	var invalidated uint64
	if !input.CacheExpired {
		switch input.CacheObservation.Status {
		case "observed":
			if input.CacheObservation.Tokens != nil {
				invalidated = *input.CacheObservation.Tokens
			}
		case "missing", "unsupported":
			invalidated = input.CurrentTokens
		default:
			invalidated = input.CurrentTokens
		}
	}
	net := signedTokenDifference(released*expectedCalls, invalidated)
	if !input.HardPressure && net <= 0 {
		result.Omissions = nil
		return result
	}
	result.ReleasedTokens = released
	result.InvalidatedCacheTokens = invalidated
	result.NetBenefitTokens = net
	if input.CurrentTokens > released {
		result.AfterTokens = input.CurrentTokens - released
	} else {
		result.AfterTokens = 0
	}
	return result
}
