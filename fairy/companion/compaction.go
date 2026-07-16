package companion

import (
	"errors"
	"strings"
	"unicode/utf8"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
)

const (
	defaultModelContextWindowTokens    uint64 = 1_048_576
	autoCompactionThresholdBasisPoints uint64 = 8_000
	basisPointsDenominator             uint64 = 10_000
	respondOutputReserveTokens         uint64 = 640
	maxCompactionSummaryChars                 = 12_000
)

type CompactionPolicy struct {
	AutoInputTokenThreshold *uint64
}

type CompactionTrigger int

const (
	CompactionTriggerManual CompactionTrigger = iota
	CompactionTriggerAfterCompletedTurn
)

func CompactionPolicyFromContextWindow(contextWindowTokens uint64) CompactionPolicy {
	if contextWindowTokens == 0 {
		contextWindowTokens = defaultModelContextWindowTokens
	}
	raw := contextWindowTokens * autoCompactionThresholdBasisPoints / basisPointsDenominator
	threshold := uint64(0)
	if raw > respondOutputReserveTokens {
		threshold = raw - respondOutputReserveTokens
	}
	return CompactionPolicy{AutoInputTokenThreshold: &threshold}
}

func (p CompactionPolicy) ShouldCompact(trigger CompactionTrigger, promptTokens uint64, usageKnown bool) bool {
	switch trigger {
	case CompactionTriggerManual:
		return true
	case CompactionTriggerAfterCompletedTurn:
		if p.AutoInputTokenThreshold == nil || !usageKnown || promptTokens == 0 {
			return false
		}
		return promptTokens >= *p.AutoInputTokenThreshold
	default:
		return false
	}
}

func (p CompactionPolicy) ShouldCompactAfterTurn(promptTokens uint64) bool {
	return p.ShouldCompact(CompactionTriggerAfterCompletedTurn, promptTokens, promptTokens > 0)
}

func normalizeCompactionSummary(summary string) (string, error) {
	value := strings.TrimSpace(summary)
	length := utf8.RuneCountInString(value)
	if length == 0 || length > maxCompactionSummaryChars {
		return "", errors.New("compaction summary must be 1-12000 characters")
	}
	return value, nil
}

// BuildCompactInput mirrors Compact lane items: current character, profile, windowed dialogue, prior summary.
func BuildCompactInput(
	record character.Record,
	userProfile *profile.Snapshot,
	promptWindow memory.PromptWindowRecord,
	messages []memory.MessageRecord,
) ([]model.PromptItem, error) {
	windowed := messagesAfterCutoff(messages, promptWindow.CutoffMessageSequence)
	items := make([]model.PromptItem, 0, len(windowed)+3)
	characterItem, err := encodeCharacterContext(record)
	if err != nil {
		return nil, err
	}
	items = append(items, characterItem)
	profileItem, err := encodeUserProfileContext(userProfile)
	if err != nil {
		return nil, err
	}
	items = append(items, profileItem)
	if promptWindow.Summary != nil && *promptWindow.Summary != "" {
		summaryItem, err := encodeCompactionSummary(*promptWindow.Summary)
		if err != nil {
			return nil, err
		}
		items = append(items, summaryItem)
	}
	items = append(items, promptItemsFromMessages(windowed)...)
	return items, nil
}

func (s *CompanionService) scheduleAutoCompaction(conversationID string, events []model.StreamEvent) {
	if s == nil || !s.RespondRuntimeMigrated() {
		return
	}
	promptTokens, known := lastPromptTokens(events)
	if !known {
		return
	}
	connection, err := config.ReadModelConnection(s.root)
	if err != nil {
		return
	}
	policy := CompactionPolicyFromContextWindow(connection.ContextWindowTokens)
	if !policy.ShouldCompact(CompactionTriggerAfterCompletedTurn, promptTokens, true) {
		return
	}
	go func() {
		s.backgroundJobs.Add(1)
		defer s.backgroundJobs.Add(-1)
		if _, err := s.CompactConversation(conversationID); err != nil {
			s.setBackgroundError(err)
			return
		}
		s.clearBackgroundError()
	}()
}

func lastPromptTokens(events []model.StreamEvent) (uint64, bool) {
	var tokens uint64
	known := false
	for _, event := range events {
		if event.Type == "usage" && event.Usage != nil {
			tokens = uint64(event.Usage.PromptTokens)
			known = event.Usage.PromptTokens > 0
		}
	}
	return tokens, known
}
