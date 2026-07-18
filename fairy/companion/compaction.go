package companion

import (
	"errors"
	"strings"
	"unicode/utf8"

	"fairy/character"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
)

const (
	defaultModelContextWindowTokens    uint64 = 1_048_576
	autoCompactionThresholdBasisPoints uint64 = 8_000
	basisPointsDenominator             uint64 = 10_000
	respondOutputReserveTokens         uint64 = 640
	compactionFailureBreakerThreshold  uint64 = 3
	estimatedPromptCharsPerToken       uint64 = 4
	maxCompactionSummaryChars                 = 12_000
)

type CompactionPolicy struct {
	AutoInputTokenThreshold *uint64
}

type CompactionTrigger int

const (
	CompactionTriggerManual CompactionTrigger = iota
	CompactionTriggerAfterCompletedTurn
	CompactionTriggerPreTurnPredictive
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
	case CompactionTriggerPreTurnPredictive:
		if p.AutoInputTokenThreshold == nil || promptTokens == 0 {
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

func (p CompactionPolicy) ShouldCompactWindow(trigger CompactionTrigger, promptTokens uint64, usageKnown bool, window *memory.ContextWindowRecord) bool {
	if trigger != CompactionTriggerManual && contextWindowBreakerOpen(window) {
		return false
	}
	return p.ShouldCompact(trigger, promptTokens, usageKnown)
}

func contextWindowBreakerOpen(window *memory.ContextWindowRecord) bool {
	return window != nil && window.FailureCount >= compactionFailureBreakerThreshold
}

func estimatePromptPrefillTokens(instructions string, input []model.PromptItem) uint64 {
	chars := uint64(utf8.RuneCountInString(instructions))
	for _, item := range input {
		chars += uint64(utf8.RuneCountInString(string(item.Type)))
		chars += uint64(utf8.RuneCountInString(item.Content))
		chars += 12
	}
	if chars == 0 {
		return 0
	}
	return (chars + estimatedPromptCharsPerToken - 1) / estimatedPromptCharsPerToken
}

func normalizeCompactionSummary(summary string) (string, error) {
	value := strings.TrimSpace(summary)
	length := utf8.RuneCountInString(value)
	if length == 0 || length > maxCompactionSummaryChars {
		return "", errors.New("compaction summary must be 1-12000 characters")
	}
	return value, nil
}

// BuildStablePrefixItems returns the respond/compact shared cacheable prefix:
// character → display_language → profile → available_visual_states.
func BuildStablePrefixItems(
	record character.Record,
	userProfile *profile.Snapshot,
	states []VisualState,
) ([]model.PromptItem, error) {
	characterItem, err := encodeCharacterContext(record)
	if err != nil {
		return nil, err
	}
	displayLanguageItem, err := encodeDisplayLanguageConstraint(record)
	if err != nil {
		return nil, err
	}
	profileItem, err := encodeUserProfileContext(userProfile)
	if err != nil {
		return nil, err
	}
	visualItem, err := encodeAvailableVisualStates(states)
	if err != nil {
		return nil, err
	}
	return []model.PromptItem{characterItem, displayLanguageItem, profileItem, visualItem}, nil
}

// BuildCompactInput mirrors respond's stable prefix, then window summary/dialogue,
// then a trailing compaction directive. Only the dialogue window is compacted.
func BuildCompactInput(
	record character.Record,
	userProfile *profile.Snapshot,
	promptWindow memory.PromptWindowRecord,
	messages []memory.MessageRecord,
	states []VisualState,
) ([]model.PromptItem, error) {
	windowed := messagesAfterCutoff(messages, promptWindow.CutoffMessageSequence)
	prefix, err := BuildStablePrefixItems(record, userProfile, states)
	if err != nil {
		return nil, err
	}
	items := make([]model.PromptItem, 0, len(prefix)+len(windowed)+2)
	items = append(items, prefix...)
	if promptWindow.Summary != nil && *promptWindow.Summary != "" {
		summaryItem, err := encodeCompactionSummary(*promptWindow.Summary)
		if err != nil {
			return nil, err
		}
		items = append(items, summaryItem)
	}
	items = append(items, promptItemsFromMessages(windowed)...)
	items = append(items, model.PromptItem{
		Type:    model.PromptItemUserMessage,
		Content: CompactInstructions,
	})
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
	connection, err := s.configReader().ModelConnection()
	if err != nil {
		return
	}
	policy := CompactionPolicyFromContextWindow(connection.ContextWindowTokens)
	window, found, err := s.memoryStore.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		s.setBackgroundError(err)
		return
	}
	var windowPtr *memory.ContextWindowRecord
	if found {
		windowPtr = &window
	}
	if !policy.ShouldCompactWindow(CompactionTriggerAfterCompletedTurn, promptTokens, true, windowPtr) {
		return
	}
	go func() {
		s.backgroundJobs.Add(1)
		defer s.backgroundJobs.Add(-1)
		if _, err := s.CompactConversation(conversationID); err != nil {
			if recordErr := s.recordContextWindowFailure(conversationID); recordErr != nil {
				s.setBackgroundError(recordErr)
				return
			}
			s.setBackgroundError(err)
			return
		}
		s.clearBackgroundError()
	}()
}

func (s *CompanionService) maybeCompactBeforeTurn(request SubmitCompiledTurnRequest) error {
	if s == nil || !s.RespondRuntimeMigrated() {
		return nil
	}
	bootstrap, err := s.memoryStore.LoadConversation(request.ConversationID)
	if err != nil {
		return err
	}
	if len(messagesAfterCutoff(bootstrap.Messages, bootstrap.PromptWindow.CutoffMessageSequence)) == 0 {
		return nil
	}
	characterRecord, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return err
	}
	userProfile, err := s.profileStore().Current()
	if err != nil {
		return err
	}
	estimatedMessages := append([]memory.MessageRecord(nil), bootstrap.Messages...)
	estimatedMessages = append(estimatedMessages, memory.MessageRecord{
		Role:     "user",
		Content:  request.Input,
		Sequence: uint64(len(estimatedMessages) + 1),
	})
	slots, err := BuildRespondContextSlots(characterRecord, userProfile, bootstrap.PromptWindow, estimatedMessages, request.AvailableVisualStates, memory.RetrievalContext{})
	if err != nil {
		return err
	}
	estimatedTokens := estimatePromptPrefillTokens(RespondInstructions, PromptItemsFromContextSlots(slots))
	window, err := s.recordEstimatedContextWindow(request.ConversationID, bootstrap.PromptWindow.Revision, estimatedTokens)
	if err != nil {
		return err
	}
	connection, err := s.configReader().ModelConnection()
	if err != nil {
		return err
	}
	policy := CompactionPolicyFromContextWindow(connection.ContextWindowTokens)
	if !policy.ShouldCompactWindow(CompactionTriggerPreTurnPredictive, estimatedTokens, true, window) {
		return nil
	}
	if _, err := s.CompactConversation(request.ConversationID); err != nil {
		if errors.Is(err, ErrTurnInProgress) {
			return nil
		}
		if recordErr := s.recordContextWindowFailure(request.ConversationID); recordErr != nil {
			return recordErr
		}
		return err
	}
	s.clearBackgroundError()
	return nil
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
