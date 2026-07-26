package compaction

import (
	"errors"
	"strings"
	"unicode/utf8"

	"fairy/memory"
)

const (
	DefaultModelContextWindowTokens uint64 = 1_048_576
	AutoThresholdBasisPoints        uint64 = 8_000
	BasisPointsDenominator          uint64 = 10_000
	RespondOutputReserveTokens      uint64 = 640
	FailureBreakerThreshold         uint64 = 3
	EstimatedPromptCharsPerToken    uint64 = 4
	MaxSummaryChars                        = 12_000
)

type Policy struct {
	AutoInputTokenThreshold *uint64
}

type Trigger int

const (
	TriggerManual Trigger = iota
	TriggerAfterCompletedTurn
	TriggerPreTurnPredictive
)

func PolicyFromContextWindow(contextWindowTokens uint64) Policy {
	if contextWindowTokens == 0 {
		contextWindowTokens = DefaultModelContextWindowTokens
	}
	raw := contextWindowTokens * AutoThresholdBasisPoints / BasisPointsDenominator
	threshold := uint64(0)
	if raw > RespondOutputReserveTokens {
		threshold = raw - RespondOutputReserveTokens
	}
	return Policy{AutoInputTokenThreshold: &threshold}
}

func (p Policy) ShouldCompact(trigger Trigger, promptTokens uint64, usageKnown bool) bool {
	switch trigger {
	case TriggerManual:
		return true
	case TriggerAfterCompletedTurn:
		if p.AutoInputTokenThreshold == nil || !usageKnown || promptTokens == 0 {
			return false
		}
		return promptTokens >= *p.AutoInputTokenThreshold
	case TriggerPreTurnPredictive:
		if p.AutoInputTokenThreshold == nil || promptTokens == 0 {
			return false
		}
		return promptTokens >= *p.AutoInputTokenThreshold
	default:
		return false
	}
}

func (p Policy) ShouldCompactAfterTurn(promptTokens uint64) bool {
	return p.ShouldCompact(TriggerAfterCompletedTurn, promptTokens, promptTokens > 0)
}

func (p Policy) ShouldCompactWindow(trigger Trigger, promptTokens uint64, usageKnown bool, window *memory.ContextWindowRecord) bool {
	if trigger != TriggerManual && ContextWindowBreakerOpen(window) {
		return false
	}
	return p.ShouldCompact(trigger, promptTokens, usageKnown)
}

func ContextWindowBreakerOpen(window *memory.ContextWindowRecord) bool {
	return window != nil && window.FailureCount >= FailureBreakerThreshold
}

func EstimatePromptTokens(charCount uint64) uint64 {
	if EstimatedPromptCharsPerToken == 0 {
		return charCount
	}
	return (charCount + EstimatedPromptCharsPerToken - 1) / EstimatedPromptCharsPerToken
}

func ValidateSummary(summary string) error {
	trimmed := strings.TrimSpace(summary)
	length := utf8.RuneCountInString(trimmed)
	if length == 0 || length > MaxSummaryChars {
		return errors.New("compaction summary must be 1-12000 characters")
	}
	return nil
}
