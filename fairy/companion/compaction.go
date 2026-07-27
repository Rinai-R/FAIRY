package companion

import (
	"errors"
	"strings"
	"unicode/utf8"

	"fairy/character"
	"fairy/compaction"
	"fairy/memory"
	"fairy/model"
	"fairy/persona"
	"fairy/profile"

	domain "fairy/interaction"
)

type CompactionPolicy = compaction.Policy
type CompactionTrigger = compaction.Trigger

const (
	CompactionTriggerManual             = compaction.TriggerManual
	CompactionTriggerAfterCompletedTurn = compaction.TriggerAfterCompletedTurn
	CompactionTriggerPreTurnPredictive  = compaction.TriggerPreTurnPredictive
	estimatedPromptCharsPerToken        = compaction.EstimatedPromptCharsPerToken
	maxCompactionSummaryChars           = compaction.MaxSummaryChars
)

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
	return compaction.EstimatePromptTokens(chars)
}

func normalizeCompactionSummary(summary string) (string, error) {
	value := strings.TrimSpace(summary)
	if err := compaction.ValidateSummary(value); err != nil {
		return "", errors.New("compaction summary must be 1-12000 characters")
	}
	return value, nil
}

// BuildCompactInput mirrors respond's stable prefix, then window summary/dialogue,
// then a trailing compaction directive. Only the dialogue window is compacted.
func BuildCompactInput(
	record character.Record,
	userProfile *profile.Snapshot,
	promptWindow memory.PromptWindowRecord,
	messages []memory.MessageRecord,
	states []VisualState,
	resolved domain.Resolved,
) ([]model.PromptItem, error) {
	windowed := messagesAfterCutoff(messages, promptWindow.CutoffMessageSequence)
	prefix, err := persona.BuildStablePrefixItems(record, userProfile, states)
	if err != nil {
		return nil, err
	}
	if !resolved.AllowsPersonalMemory() {
		prefix = append(prefix[:2], prefix[3:]...)
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
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		return
	}
	policy := compaction.PolicyFromContextWindow(connection.ContextWindowTokens)
	window, found, err := s.memory.turn.runtimeState.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
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
	if s.retention == nil || !s.retention.Run(func() {
		if _, err := s.CompactConversation(conversationID); err != nil {
			if recordErr := s.recordContextWindowFailure(conversationID); recordErr != nil {
				s.setBackgroundError(recordErr)
				return
			}
			s.setBackgroundError(err)
			return
		}
		s.clearBackgroundError()
	}) {
		return
	}
}

type preTurnPreparation struct {
	visualStates  []VisualState
	compactionErr error
}

func (s *CompanionService) prepareBeforeTurn(
	request SubmitCompiledTurnRequest,
	resolved domain.Resolved,
	deriveVisualStates bool,
) (preTurnPreparation, error) {
	result := preTurnPreparation{visualStates: request.AvailableVisualStates}
	if s == nil || !s.RespondRuntimeMigrated() {
		return result, nil
	}
	bootstrap, err := s.memory.turn.promptContext.LoadConversationPrompt(request.ConversationID)
	if err != nil {
		if deriveVisualStates {
			return result, err
		}
		result.compactionErr = err
		return result, nil
	}
	windowed := messagesAfterCutoff(bootstrap.Messages, bootstrap.PromptWindow.CutoffMessageSequence)
	if len(windowed) == 0 && !deriveVisualStates {
		return result, nil
	}
	characterRecord, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		if deriveVisualStates {
			return result, err
		}
		result.compactionErr = err
		return result, nil
	}
	if deriveVisualStates {
		result.visualStates, err = visualStatesFromCharacter(characterRecord)
		if err != nil {
			return result, err
		}
	}
	if len(windowed) == 0 {
		return result, nil
	}
	var userProfile *profile.Snapshot
	if resolved.AllowsPersonalMemory() {
		userProfile, err = s.profileSource().Current()
		if err != nil {
			result.compactionErr = err
			return result, nil
		}
	}
	estimatedMessages := append([]memory.MessageRecord(nil), bootstrap.Messages...)
	if request.Initiation == nil {
		estimatedMessages = append(estimatedMessages, memory.MessageRecord{
			Role: "user", Content: request.Input, Sequence: uint64(len(estimatedMessages) + 1),
		})
	}
	slots, err := persona.BuildRespondContextSlots(characterRecord, userProfile, bootstrap.PromptWindow, estimatedMessages, result.visualStates, memory.RetrievalContext{}, resolved)
	if err != nil {
		result.compactionErr = err
		return result, nil
	}
	if request.Initiation != nil {
		slots, err = AppendDesktopInitiationContext(slots, *request.Initiation)
		if err != nil {
			result.compactionErr = err
			return result, nil
		}
	}
	estimatedTokens := estimatePromptPrefillTokens(RespondInstructions, persona.PromptItemsFromContextSlots(slots))
	window, err := s.recordEstimatedContextWindow(request.ConversationID, bootstrap.PromptWindow.Revision, estimatedTokens)
	if err != nil {
		result.compactionErr = err
		return result, nil
	}
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		result.compactionErr = err
		return result, nil
	}
	policy := compaction.PolicyFromContextWindow(connection.ContextWindowTokens)
	if !policy.ShouldCompactWindow(CompactionTriggerPreTurnPredictive, estimatedTokens, true, window) {
		return result, nil
	}
	if _, err := s.CompactConversation(request.ConversationID); err != nil {
		if errors.Is(err, ErrTurnInProgress) {
			return result, nil
		}
		if recordErr := s.recordContextWindowFailure(request.ConversationID); recordErr != nil {
			result.compactionErr = recordErr
			return result, nil
		}
		result.compactionErr = err
		return result, nil
	}
	s.clearBackgroundError()
	return result, nil
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
