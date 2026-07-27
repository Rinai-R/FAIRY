package companion

import (
	"errors"
	"strings"
	"unicode/utf8"

	"fairy/memory"
)

const (
	defaultModelContextWindowTokens uint64 = 1_048_576
	autoThresholdBasisPoints        uint64 = 8_000
	basisPointsDenominator          uint64 = 10_000
	respondOutputReserveTokens      uint64 = 640
	failureBreakerThreshold         uint64 = 3
	estimatedPromptCharsPerToken    uint64 = 4
	maxCompactionSummaryChars              = 12_000
)

type compactionPolicy struct {
	AutoInputTokenThreshold *uint64
}

type compactionTrigger int

const (
	compactionTriggerManual compactionTrigger = iota
	compactionTriggerAfterCompletedTurn
	compactionTriggerPreTurnPredictive
)

func compactionPolicyFromContextWindow(contextWindowTokens uint64) compactionPolicy {
	if contextWindowTokens == 0 {
		contextWindowTokens = defaultModelContextWindowTokens
	}
	raw := contextWindowTokens * autoThresholdBasisPoints / basisPointsDenominator
	threshold := uint64(0)
	if raw > respondOutputReserveTokens {
		threshold = raw - respondOutputReserveTokens
	}
	return compactionPolicy{AutoInputTokenThreshold: &threshold}
}

func (p compactionPolicy) shouldCompact(trigger compactionTrigger, promptTokens uint64, usageKnown bool) bool {
	switch trigger {
	case compactionTriggerManual:
		return true
	case compactionTriggerAfterCompletedTurn:
		if p.AutoInputTokenThreshold == nil || !usageKnown || promptTokens == 0 {
			return false
		}
		return promptTokens >= *p.AutoInputTokenThreshold
	case compactionTriggerPreTurnPredictive:
		if p.AutoInputTokenThreshold == nil || promptTokens == 0 {
			return false
		}
		return promptTokens >= *p.AutoInputTokenThreshold
	default:
		return false
	}
}

func (p compactionPolicy) shouldCompactAfterTurn(promptTokens uint64) bool {
	return p.shouldCompact(compactionTriggerAfterCompletedTurn, promptTokens, promptTokens > 0)
}

func (p compactionPolicy) shouldCompactWindow(trigger compactionTrigger, promptTokens uint64, usageKnown bool, window *memory.ContextWindowRecord) bool {
	if trigger != compactionTriggerManual && contextWindowBreakerOpen(window) {
		return false
	}
	return p.shouldCompact(trigger, promptTokens, usageKnown)
}

func contextWindowBreakerOpen(window *memory.ContextWindowRecord) bool {
	return window != nil && window.FailureCount >= failureBreakerThreshold
}

func estimatePromptTokens(charCount uint64) uint64 {
	if estimatedPromptCharsPerToken == 0 {
		return charCount
	}
	return (charCount + estimatedPromptCharsPerToken - 1) / estimatedPromptCharsPerToken
}

func validateCompactionSummary(summary string) error {
	trimmed := strings.TrimSpace(summary)
	length := utf8.RuneCountInString(trimmed)
	if length == 0 || length > maxCompactionSummaryChars {
		return errors.New("compaction summary must be 1-12000 characters")
	}
	return nil
}
