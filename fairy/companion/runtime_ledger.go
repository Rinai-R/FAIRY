package companion

import (
	"encoding/json"
	"strings"

	"fairy/memory"
	"fairy/model"
)

const (
	runtimeLedgerEventTransition    = "transition"
	runtimeLedgerEventPrompt        = "prompt"
	runtimeLedgerEventContinuation  = "continuation"
	runtimeLedgerEventModel         = "model"
	runtimeLedgerEventContextWindow = "context_window"
	runtimeLedgerEventCompile       = "compile"
	runtimeLedgerEventSpeech        = "speech"
	runtimeLedgerEventBeatDelivery  = "beat_delivery"
	runtimeLedgerEventTerminal      = "terminal"
)

func (s *CompanionService) appendRuntimeLedger(conversationID string, turnID string, eventType string, state TurnState, code string, metadata map[string]any) {
	if s == nil || s.memory == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		s.setBackgroundError(err)
		return
	}
	var stateValue *string
	if state != "" {
		value := string(state)
		stateValue = &value
	}
	var codeValue *string
	if code != "" {
		codeValue = &code
	}
	if _, err := s.memory.AppendTurnRuntimeEvent(memory.TurnRuntimeEventInput{
		ConversationID: conversationID,
		TurnID:         turnID,
		EventType:      eventType,
		State:          stateValue,
		Code:           codeValue,
		MetadataJSON:   string(encoded),
	}); err != nil {
		s.setBackgroundError(err)
	}
}

func runtimeBeatDeliveryLedgerMetadata(status, kind string, chainIndex, playIndex int, targetIntervalMS, paceWaitMS int64, prefixCount int) map[string]any {
	return map[string]any{
		"status":               status,
		"kind":                 kind,
		"chainIndex":           chainIndex,
		"playIndex":            playIndex,
		"targetIntervalMs":     targetIntervalMS,
		"paceWaitMs":           paceWaitMS,
		"publishedPrefixCount": prefixCount,
	}
}

func runtimePromptLedgerMetadata(
	input []model.PromptItem,
	slots []ContextSlot,
	promptWindow memory.PromptWindowRecord,
	messages []memory.MessageRecord,
	visualStates []VisualState,
	retrieval memory.RetrievalContext,
) map[string]any {
	metadata := map[string]any{
		"promptInputHash":            runtimeHash(input),
		"promptItemCount":            len(input),
		"promptWindowRevision":       promptWindow.Revision,
		"promptWindowSummaryPresent": promptWindow.Summary != nil,
		"dialogueMessageCount":       len(messages),
		"availableVisualStateCount":  len(visualStates),
		"retrievedPersonalCount":     len(retrieval.PersonalMemories),
		"retrievedKnowledgeCount":    len(retrieval.Knowledge),
		"contextSlots":               runtimeContextSlotMetadata(slots),
	}
	return metadata
}

func runtimeContextSlotMetadata(slots []ContextSlot) []map[string]any {
	metadata := make([]map[string]any, 0, len(slots))
	for _, slot := range slots {
		entry := map[string]any{
			"id":           slot.ID,
			"required":     slot.Required,
			"trust":        slot.Trust,
			"cachePolicy":  slot.CachePolicy,
			"revisionHash": slot.RevisionHash,
			"present":      slot.Present,
			"itemCount":    len(slot.Items),
		}
		if slot.OmitReason != "" {
			entry["omitReason"] = slot.OmitReason
		}
		metadata = append(metadata, entry)
	}
	return metadata
}

func runtimeContinuationLedgerMetadata(cacheRetention bool, previous *memory.LaneContinuationRecord, fullRequest model.CompiledPromptRequest, executeRequest model.CompiledPromptRequest, continuation model.ContinuationDecision) map[string]any {
	metadata := map[string]any{
		"cacheRetentionSupported": cacheRetention,
		"incremental":             continuation.Incremental,
		"fullReason":              string(continuation.FullReason),
		"previousStatePresent":    previous != nil,
		"previousStateSource":     "sqlite_lane_continuations",
		"requestShapeHash":        runtimeHash(fullRequest.Shape),
		"fullInputHash":           runtimeHash(fullRequest.Input),
		"fullInputItemCount":      len(fullRequest.Input),
		"executeInputItemCount":   len(executeRequest.Input),
	}
	if previous != nil {
		metadata["storedWindowRevision"] = previous.WindowRevision
		metadata["storedRequestShapeHash"] = previous.RequestShapeHash
		metadata["storedInputPrefixHash"] = previous.InputPrefixHash
		metadata["storedResponseItemHash"] = previous.ResponseItemHash
	}
	if continuation.Incremental {
		metadata["newItemCount"] = len(continuation.NewItems)
		metadata["newItemsHash"] = runtimeHash(continuation.NewItems)
		metadata["previousResponseIDPresent"] = continuation.PreviousResponseID != ""
		metadata["previousResponseIDHash"] = runtimeHash(continuation.PreviousResponseID)
	} else if previous != nil {
		metadata["previousResponseIDPresent"] = previous.PreviousResponseID != ""
		metadata["previousResponseIDHash"] = runtimeHash(previous.PreviousResponseID)
	}
	return metadata
}

func runtimeModelLedgerMetadata(events []model.StreamEvent, usage []LaneModelUsage) map[string]any {
	responseID := model.ResponseIDFromEvents(events)
	return map[string]any{
		"streamEventCount":         len(events),
		"responseIDPresent":        responseID != "",
		"responseIDHash":           runtimeHash(responseID),
		"usage":                    usage,
		"providerCacheObservation": "provider_fields_only",
	}
}

func runtimeTerminalLedgerMetadata(status string, reply CompiledReply, usage []LaneModelUsage) map[string]any {
	return map[string]any{
		"status":          status,
		"visualState":     reply.VisualState,
		"chainCount":      len(reply.Chains),
		"displayTextHash": runtimeHash(reply.DisplayText),
		"speechTextHash":  runtimeHash(reply.SpeechText),
		"usage":           usage,
	}
}

func runtimeFailureLedgerMetadata(code string, cause error, retryable bool) map[string]any {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return map[string]any{
		"status":      "failed",
		"errorCode":   code,
		"retryable":   retryable,
		"messageHash": runtimeHash(message),
	}
}

func runtimeInterruptedTerminalLedgerMetadata(plannedCount int, published []ReplyChain) map[string]any {
	parts := make([]string, 0, len(published))
	for _, chain := range published {
		parts = append(parts, chain.Text)
	}
	prefix := strings.Join(parts, "\n")
	return map[string]any{
		"status":              "interrupted",
		"plannedChainCount":   plannedCount,
		"publishedChainCount": len(published),
		"publishedPrefixHash": runtimeHash(prefix),
	}
}
