package companion

import (
	"fmt"

	"fairy/memory"
	"fairy/model"
)

const (
	contextWindowTriggerCompletedUsage   = "completed_usage"
	contextWindowTriggerPreTurnEstimate  = "pre_turn_estimate"
	contextWindowTriggerCompactionFailed = "compaction_failed"
	contextWindowTriggerCompactionCommit = "compaction_committed"
	contextWindowTriggerCreated          = "created"
)

func (s *CompanionService) recordObservedContextWindow(
	conversationID string,
	promptWindowRevision uint64,
	usage []LaneModelUsage,
) (*memory.ContextWindowRecord, error) {
	if s == nil || s.memoryStore == nil || promptWindowRevision == 0 {
		return nil, nil
	}
	prefill, ok := respondInputTokens(usage)
	if !ok {
		return nil, nil
	}
	existing, found, err := s.memoryStore.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return nil, err
	}
	record := nextObservedContextWindowRecord(conversationID, promptWindowRevision, prefill, existing, found)
	saved, err := s.memoryStore.SaveContextWindow(record)
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

func respondInputTokens(usage []LaneModelUsage) (uint64, bool) {
	for _, lane := range usage {
		if lane.Lane != string(model.PromptLaneRespond) || lane.Usage.InputTokens == nil {
			continue
		}
		return *lane.Usage.InputTokens, true
	}
	return 0, false
}

func (s *CompanionService) recordEstimatedContextWindow(
	conversationID string,
	promptWindowRevision uint64,
	estimated uint64,
) (*memory.ContextWindowRecord, error) {
	if s == nil || s.memoryStore == nil || promptWindowRevision == 0 || estimated == 0 {
		return nil, nil
	}
	existing, found, err := s.memoryStore.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return nil, err
	}
	record := nextEstimatedContextWindowRecord(conversationID, promptWindowRevision, estimated, existing, found)
	saved, err := s.memoryStore.SaveContextWindow(record)
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

func (s *CompanionService) recordContextWindowFailure(conversationID string) error {
	if s == nil || s.memoryStore == nil {
		return nil
	}
	existing, found, err := s.memoryStore.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	existing.FailureCount++
	existing.LastTrigger = contextWindowTriggerCompactionFailed
	_, err = s.memoryStore.SaveContextWindow(existing)
	return err
}

func (s *CompanionService) advanceContextWindowAfterCompaction(conversationID string, promptWindowRevision uint64) (*memory.ContextWindowRecord, error) {
	if s == nil || s.memoryStore == nil || promptWindowRevision == 0 {
		return nil, nil
	}
	existing, found, err := s.memoryStore.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return nil, err
	}
	record := nextCompactionCommittedContextWindowRecord(conversationID, promptWindowRevision, existing, found)
	saved, err := s.memoryStore.SaveContextWindow(record)
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

func nextObservedContextWindowRecord(
	conversationID string,
	promptWindowRevision uint64,
	prefill uint64,
	existing memory.ContextWindowRecord,
	found bool,
) memory.ContextWindowRecord {
	observed := prefill
	if !found {
		windowID := contextWindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
		return memory.ContextWindowRecord{
			ConversationID:        conversationID,
			Lane:                  string(model.PromptLaneRespond),
			WindowNumber:          1,
			FirstWindowID:         windowID,
			WindowID:              windowID,
			ObservedPrefillTokens: &observed,
			LastTrigger:           contextWindowTriggerCompletedUsage,
			PromptWindowRevision:  promptWindowRevision,
		}
	}
	if existing.PromptWindowRevision == promptWindowRevision {
		existing.ObservedPrefillTokens = &observed
		existing.LastTrigger = contextWindowTriggerCompletedUsage
		return existing
	}
	previousWindowID := existing.WindowID
	windowID := contextWindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
	return memory.ContextWindowRecord{
		ConversationID:        conversationID,
		Lane:                  string(model.PromptLaneRespond),
		WindowNumber:          existing.WindowNumber + 1,
		FirstWindowID:         existing.FirstWindowID,
		PreviousWindowID:      &previousWindowID,
		WindowID:              windowID,
		ObservedPrefillTokens: &observed,
		LastTrigger:           contextWindowTriggerCompletedUsage,
		PromptWindowRevision:  promptWindowRevision,
	}
}

func nextEstimatedContextWindowRecord(
	conversationID string,
	promptWindowRevision uint64,
	estimatedTokens uint64,
	existing memory.ContextWindowRecord,
	found bool,
) memory.ContextWindowRecord {
	estimated := estimatedTokens
	if !found {
		windowID := contextWindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
		return memory.ContextWindowRecord{
			ConversationID:         conversationID,
			Lane:                   string(model.PromptLaneRespond),
			WindowNumber:           1,
			FirstWindowID:          windowID,
			WindowID:               windowID,
			EstimatedPrefillTokens: &estimated,
			LastTrigger:            contextWindowTriggerPreTurnEstimate,
			PromptWindowRevision:   promptWindowRevision,
		}
	}
	if existing.PromptWindowRevision == promptWindowRevision {
		existing.EstimatedPrefillTokens = &estimated
		existing.LastTrigger = contextWindowTriggerPreTurnEstimate
		return existing
	}
	previousWindowID := existing.WindowID
	windowID := contextWindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
	return memory.ContextWindowRecord{
		ConversationID:         conversationID,
		Lane:                   string(model.PromptLaneRespond),
		WindowNumber:           existing.WindowNumber + 1,
		FirstWindowID:          existing.FirstWindowID,
		PreviousWindowID:       &previousWindowID,
		WindowID:               windowID,
		EstimatedPrefillTokens: &estimated,
		LastTrigger:            contextWindowTriggerPreTurnEstimate,
		PromptWindowRevision:   promptWindowRevision,
	}
}

func nextCompactionCommittedContextWindowRecord(
	conversationID string,
	promptWindowRevision uint64,
	existing memory.ContextWindowRecord,
	found bool,
) memory.ContextWindowRecord {
	windowID := contextWindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
	if !found {
		return memory.ContextWindowRecord{
			ConversationID:       conversationID,
			Lane:                 string(model.PromptLaneRespond),
			WindowNumber:         1,
			FirstWindowID:        windowID,
			WindowID:             windowID,
			LastTrigger:          contextWindowTriggerCompactionCommit,
			PromptWindowRevision: promptWindowRevision,
		}
	}
	if existing.PromptWindowRevision == promptWindowRevision {
		existing.ObservedPrefillTokens = nil
		existing.EstimatedPrefillTokens = nil
		existing.LastTrigger = contextWindowTriggerCompactionCommit
		existing.FailureCount = 0
		return existing
	}
	previousWindowID := existing.WindowID
	firstWindowID := existing.FirstWindowID
	if firstWindowID == "" {
		firstWindowID = windowID
	}
	return memory.ContextWindowRecord{
		ConversationID:       conversationID,
		Lane:                 string(model.PromptLaneRespond),
		WindowNumber:         existing.WindowNumber + 1,
		FirstWindowID:        firstWindowID,
		PreviousWindowID:     &previousWindowID,
		WindowID:             windowID,
		LastTrigger:          contextWindowTriggerCompactionCommit,
		PromptWindowRevision: promptWindowRevision,
	}
}

func contextWindowID(conversationID string, lane string, promptWindowRevision uint64) string {
	return runtimeHash(fmt.Sprintf("%s:%s:%d", conversationID, lane, promptWindowRevision))
}

func runtimeContextWindowLedgerMetadata(record *memory.ContextWindowRecord) map[string]any {
	if record == nil {
		return map[string]any{
			"recorded": false,
		}
	}
	metadata := map[string]any{
		"recorded":             true,
		"lane":                 record.Lane,
		"windowNumber":         record.WindowNumber,
		"firstWindowIDHash":    runtimeHash(record.FirstWindowID),
		"windowIDHash":         runtimeHash(record.WindowID),
		"lastTrigger":          record.LastTrigger,
		"failureCount":         record.FailureCount,
		"promptWindowRevision": record.PromptWindowRevision,
	}
	if record.PreviousWindowID != nil {
		metadata["previousWindowIDHash"] = runtimeHash(*record.PreviousWindowID)
	}
	if record.ObservedPrefillTokens != nil {
		metadata["observedPrefillTokens"] = *record.ObservedPrefillTokens
	}
	if record.EstimatedPrefillTokens != nil {
		metadata["estimatedPrefillTokens"] = *record.EstimatedPrefillTokens
	}
	return metadata
}
