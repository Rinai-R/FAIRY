package conversation

import (
	"fairy/agent/conversation/contextplan"
	historyruntime "fairy/context/history/runtime"
	"fairy/runtime/model"
)

func (s *Service) recordObservedContextWindow(
	conversationID string,
	promptWindowRevision uint64,
	usage []LaneModelUsage,
) (*historyruntime.ContextWindowRecord, error) {
	if s == nil || s.memory.turn.runtimeState == nil || promptWindowRevision == 0 {
		return nil, nil
	}
	prefill, ok := respondInputTokens(usage)
	if !ok {
		return nil, nil
	}
	existing, found, err := s.memory.turn.runtimeState.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return nil, err
	}
	record := contextplan.NextObservedWindow(conversationID, promptWindowRevision, prefill, existing, found)
	saved, err := s.memory.turn.runtimeState.SaveContextWindow(record)
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

func (s *Service) recordEstimatedContextWindow(
	conversationID string,
	promptWindowRevision uint64,
	estimated uint64,
) (*historyruntime.ContextWindowRecord, error) {
	if s == nil || s.memory.turn.runtimeState == nil || promptWindowRevision == 0 || estimated == 0 {
		return nil, nil
	}
	existing, found, err := s.memory.turn.runtimeState.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return nil, err
	}
	record := contextplan.NextEstimatedWindow(conversationID, promptWindowRevision, estimated, existing, found)
	saved, err := s.memory.turn.runtimeState.SaveContextWindow(record)
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

func (s *Service) recordContextWindowFailure(conversationID string) error {
	if s == nil || s.memory.turn.runtimeState == nil {
		return nil
	}
	existing, found, err := s.memory.turn.runtimeState.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	existing.FailureCount++
	existing.LastTrigger = contextplan.WindowTriggerCompactionFailed
	_, err = s.memory.turn.runtimeState.SaveContextWindow(existing)
	return err
}
