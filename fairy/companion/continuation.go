package companion

import (
	"fairy/memory"
	"fairy/model"
)

func (s *CompanionService) decideContinuation(
	conversationID string,
	cacheRetention bool,
	promptWindowRevision uint64,
	fullRequest model.CompiledPromptRequest,
) (model.ContinuationDecision, *memory.LaneContinuationRecord, error) {
	if !cacheRetention {
		return model.ContinuationDecision{FullReason: model.ContinuationCapabilityUnsupported}, nil, nil
	}
	record, ok, err := s.memory.LoadLaneContinuation(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return model.ContinuationDecision{}, nil, err
	}
	if !ok {
		return model.ContinuationDecision{FullReason: model.ContinuationNoPreviousState}, nil, nil
	}
	if err := model.ValidatePreviousResponseID(record.PreviousResponseID); err != nil {
		return model.ContinuationDecision{FullReason: model.ContinuationPreviousResponseIncomplete}, &record, nil
	}
	if record.WindowRevision != promptWindowRevision {
		return model.ContinuationDecision{FullReason: model.ContinuationPrefixMismatch}, &record, nil
	}
	if record.RequestShapeHash != runtimeHash(fullRequest.Shape) {
		return model.ContinuationDecision{FullReason: model.ContinuationRequestShapeChanged}, &record, nil
	}
	suffix, found, notExtended := continuationSuffixFromHashes(record, fullRequest.Input)
	if !found {
		return model.ContinuationDecision{FullReason: model.ContinuationPrefixMismatch}, &record, nil
	}
	if notExtended {
		return model.ContinuationDecision{FullReason: model.ContinuationInputNotExtended}, &record, nil
	}
	return model.ContinuationDecision{
		Incremental:        true,
		PreviousResponseID: record.PreviousResponseID,
		NewItems:           suffix,
	}, &record, nil
}

func continuationSuffixFromHashes(record memory.LaneContinuationRecord, input []model.PromptItem) ([]model.PromptItem, bool, bool) {
	for prefixLen := 0; prefixLen < len(input); prefixLen++ {
		if runtimeHash(input[:prefixLen]) != record.InputPrefixHash {
			continue
		}
		responseItem := []model.PromptItem{input[prefixLen]}
		if runtimeHash(responseItem) != record.ResponseItemHash {
			continue
		}
		suffix := input[prefixLen+1:]
		if len(suffix) == 0 {
			return nil, true, true
		}
		return append([]model.PromptItem(nil), suffix...), true, false
	}
	return nil, false, false
}

func (s *CompanionService) clearContinuationState(conversationID string) error {
	if s == nil || s.memory == nil {
		return nil
	}
	return s.memory.ClearLaneContinuation(conversationID, string(model.PromptLaneRespond))
}

func (s *CompanionService) updateContinuationState(
	conversationID string,
	cacheRetention bool,
	promptWindowRevision uint64,
	fullRequest model.CompiledPromptRequest,
	displayText string,
	events []model.StreamEvent,
) error {
	if s == nil || s.memory == nil {
		return nil
	}
	if !cacheRetention {
		return s.clearContinuationState(conversationID)
	}
	responseID := model.ResponseIDFromEvents(events)
	if err := model.ValidatePreviousResponseID(responseID); err != nil {
		return s.clearContinuationState(conversationID)
	}
	responseItems := []model.PromptItem{{
		Type:    model.PromptItemAssistantMessage,
		Content: displayText,
	}}
	_, err := s.memory.SaveLaneContinuation(memory.LaneContinuationRecord{
		ConversationID:     conversationID,
		Lane:               string(model.PromptLaneRespond),
		PreviousResponseID: responseID,
		RequestShapeHash:   runtimeHash(fullRequest.Shape),
		InputPrefixHash:    runtimeHash(fullRequest.Input),
		ResponseItemHash:   runtimeHash(responseItems),
		WindowRevision:     promptWindowRevision,
	})
	return err
}
