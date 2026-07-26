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
	window, found, err := s.memory.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
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
	bootstrap, err := s.memory.LoadConversation(request.ConversationID)
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
	resolved, err := s.ResolveInteraction(request.ConversationID)
	if err != nil {
		return err
	}
	var userProfile *profile.Snapshot
	if resolved.AllowsPersonalMemory() {
		userProfile, err = s.profileSource().Current()
		if err != nil {
			return err
		}
	}
	estimatedMessages := append([]memory.MessageRecord(nil), bootstrap.Messages...)
	if request.Initiation == nil {
		estimatedMessages = append(estimatedMessages, memory.MessageRecord{
			Role: "user", Content: request.Input, Sequence: uint64(len(estimatedMessages) + 1),
		})
	}
	slots, err := persona.BuildRespondContextSlots(characterRecord, userProfile, bootstrap.PromptWindow, estimatedMessages, request.AvailableVisualStates, memory.RetrievalContext{}, resolved)
	if err != nil {
		return err
	}
	if request.Initiation != nil {
		slots, err = AppendDesktopInitiationContext(slots, *request.Initiation)
		if err != nil {
			return err
		}
	}
	estimatedTokens := estimatePromptPrefillTokens(RespondInstructions, persona.PromptItemsFromContextSlots(slots))
	window, err := s.recordEstimatedContextWindow(request.ConversationID, bootstrap.PromptWindow.Revision, estimatedTokens)
	if err != nil {
		return err
	}
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		return err
	}
	policy := compaction.PolicyFromContextWindow(connection.ContextWindowTokens)
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
