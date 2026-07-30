package companion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/persona"

	"fairy/session"
)

type compactSummary struct {
	CurrentGoal     string   `json:"currentGoal"`
	UserConstraints string   `json:"userConstraints"`
	Relationship    string   `json:"relationship"`
	KeyFacts        []string `json:"keyFacts"`
	CompletedWork   string   `json:"completedWork"`
	OpenQuestions   string   `json:"openQuestions"`
	NextSteps       string   `json:"nextSteps"`
	SourceRefs      []string `json:"sourceRefs"`
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
	return estimatePromptTokens(chars)
}

func normalizeCompactionSummary(summary string) (string, error) {
	value := strings.TrimSpace(summary)
	if err := validateCompactionSummary(value); err != nil {
		return "", err
	}
	var decoded compactSummary
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return "", fmt.Errorf("decoding compaction summary: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("compaction summary contains trailing data")
	}
	if strings.TrimSpace(decoded.CurrentGoal) == "" ||
		strings.TrimSpace(decoded.UserConstraints) == "" ||
		strings.TrimSpace(decoded.Relationship) == "" ||
		decoded.KeyFacts == nil ||
		strings.TrimSpace(decoded.CompletedWork) == "" ||
		strings.TrimSpace(decoded.OpenQuestions) == "" ||
		strings.TrimSpace(decoded.NextSteps) == "" ||
		decoded.SourceRefs == nil {
		return "", errors.New("compaction summary is missing required sections")
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("encoding compaction summary: %w", err)
	}
	return string(encoded), nil
}

// buildCompactInput mirrors respond's stable prefix, then window summary/dialogue,
// then a trailing compaction directive. Only the dialogue window is compacted.
func buildCompactInput(
	record character.Record,
	userProfile *config.ProfileSnapshot,
	promptWindow memory.PromptWindowRecord,
	messages []memory.MessageRecord,
	states []VisualState,
	resolved session.Resolved,
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
	if s == nil || !s.TurnRuntimeReady() {
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
	policy := compactionPolicyFromContextWindow(connection.ContextWindowTokens)
	window, found, err := s.memory.turn.runtimeState.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		s.setBackgroundError(err)
		return
	}
	var windowPtr *memory.ContextWindowRecord
	if found {
		windowPtr = &window
	}
	if !policy.shouldCompactWindow(compactionTriggerAfterCompletedTurn, promptTokens, true, windowPtr) {
		return
	}
	if s.retention == nil {
		return
	}
	if err := s.retention.run(func() {
		cacheObservation := lastCacheObservation(events)
		if err := s.coordinatePressureCompaction(conversationID, promptTokens, cacheObservation, false); err != nil {
			if recordErr := s.recordContextWindowFailure(conversationID); recordErr != nil {
				s.setBackgroundError(recordErr)
				return
			}
			s.setBackgroundError(err)
			return
		}
		s.clearBackgroundError()
	}); err != nil {
		s.setBackgroundError(err)
		return
	}
}

type preTurnPreparation struct {
	visualStates  []VisualState
	compactionErr error
}

func (s *CompanionService) prepareBeforeTurn(
	request SubmitCompiledTurnRequest,
	resolved session.Resolved,
	deriveVisualStates bool,
) (preTurnPreparation, error) {
	result := preTurnPreparation{visualStates: request.AvailableVisualStates}
	if s == nil || !s.TurnRuntimeReady() {
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
	var userProfile *config.ProfileSnapshot
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
	policy := compactionPolicyFromContextWindow(connection.ContextWindowTokens)
	if !policy.shouldCompactWindow(compactionTriggerPreTurnPredictive, estimatedTokens, true, window) {
		return result, nil
	}
	if err := s.coordinatePressureCompaction(
		request.ConversationID, estimatedTokens, model.CacheMissing(), true,
	); err != nil {
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

func lastCacheObservation(events []model.StreamEvent) model.CachedTokenObservation {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "usage" || event.Usage == nil {
			continue
		}
		if event.Usage.CachedInputTokens == nil {
			return model.CacheMissing()
		}
		return model.CacheObserved(*event.Usage.CachedInputTokens)
	}
	return model.CacheMissing()
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
