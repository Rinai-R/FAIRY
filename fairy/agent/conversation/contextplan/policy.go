package contextplan

import (
	"errors"
	"strings"
	"unicode/utf8"

	historyruntime "fairy/context/history/runtime"
)

const (
	DefaultModelContextWindowTokens uint64 = 1_048_576
	TargetWatermarkBasisPoints      uint64 = 6_500
	SoftWatermarkBasisPoints        uint64 = 7_500
	HardWatermarkBasisPoints        uint64 = 9_000
	BasisPointsDenominator          uint64 = 10_000
	RespondOutputReserveTokens      uint64 = 640
	FailureBreakerThreshold         uint64 = 3
	EstimatedPromptCharsPerToken    uint64 = 4
	MaxCompactionSummaryChars              = 12_000
)

type Policy struct {
	AutoInputTokenThreshold *uint64
	TargetInputTokens       uint64
	SoftInputTokens         uint64
	HardInputTokens         uint64
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
	target := watermarkTokens(contextWindowTokens, TargetWatermarkBasisPoints)
	soft := watermarkTokens(contextWindowTokens, SoftWatermarkBasisPoints)
	hard := watermarkTokens(contextWindowTokens, HardWatermarkBasisPoints)
	return Policy{
		AutoInputTokenThreshold: &soft,
		TargetInputTokens:       target,
		SoftInputTokens:         soft,
		HardInputTokens:         hard,
	}
}

func watermarkTokens(contextWindowTokens, basisPoints uint64) uint64 {
	raw := contextWindowTokens * basisPoints / BasisPointsDenominator
	if raw <= RespondOutputReserveTokens {
		return 0
	}
	return raw - RespondOutputReserveTokens
}

func (p Policy) HardPressure(tokens uint64) bool {
	return p.HardInputTokens > 0 && tokens >= p.HardInputTokens
}

func (p Policy) ShouldCompact(trigger Trigger, promptTokens uint64, usageKnown bool) bool {
	switch trigger {
	case TriggerManual:
		return true
	case TriggerAfterCompletedTurn:
		return p.AutoInputTokenThreshold != nil && usageKnown && promptTokens > 0 && promptTokens >= *p.AutoInputTokenThreshold
	case TriggerPreTurnPredictive:
		return p.AutoInputTokenThreshold != nil && promptTokens > 0 && promptTokens >= *p.AutoInputTokenThreshold
	default:
		return false
	}
}

func (p Policy) ShouldCompactAfterTurn(promptTokens uint64) bool {
	return p.ShouldCompact(TriggerAfterCompletedTurn, promptTokens, promptTokens > 0)
}

func (p Policy) ShouldCompactWindow(trigger Trigger, promptTokens uint64, usageKnown bool, window *historyruntime.ContextWindowRecord) bool {
	if trigger != TriggerManual && window != nil && window.FailureCount >= FailureBreakerThreshold {
		return false
	}
	return p.ShouldCompact(trigger, promptTokens, usageKnown)
}

func EstimatePromptTokens(charCount uint64) uint64 {
	return (charCount + EstimatedPromptCharsPerToken - 1) / EstimatedPromptCharsPerToken
}

func ValidateCompactionSummary(summary string) error {
	length := utf8.RuneCountInString(strings.TrimSpace(summary))
	if length == 0 || length > MaxCompactionSummaryChars {
		return errors.New("compaction summary must be 1-12000 characters")
	}
	return nil
}
