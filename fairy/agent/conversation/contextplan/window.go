package contextplan

import (
	"fmt"

	"fairy/context/character"
	historyruntime "fairy/context/history/runtime"
	"fairy/runtime/model"
)

const (
	WindowTriggerCompletedUsage   = "completed_usage"
	WindowTriggerPreTurnEstimate  = "pre_turn_estimate"
	WindowTriggerCompactionFailed = "compaction_failed"
	WindowTriggerCompactionCommit = "compaction_committed"
)

func NextObservedWindow(conversationID string, promptWindowRevision, prefill uint64, existing historyruntime.ContextWindowRecord, found bool) historyruntime.ContextWindowRecord {
	observed := prefill
	if !found {
		windowID := WindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
		return historyruntime.ContextWindowRecord{
			ConversationID: conversationID, Lane: string(model.PromptLaneRespond), WindowNumber: 1,
			FirstWindowID: windowID, WindowID: windowID, ObservedPrefillTokens: &observed,
			LastTrigger: WindowTriggerCompletedUsage, PromptWindowRevision: promptWindowRevision,
		}
	}
	if existing.PromptWindowRevision == promptWindowRevision {
		existing.ObservedPrefillTokens = &observed
		existing.LastTrigger = WindowTriggerCompletedUsage
		return existing
	}
	previousWindowID := existing.WindowID
	windowID := WindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
	return historyruntime.ContextWindowRecord{
		ConversationID: conversationID, Lane: string(model.PromptLaneRespond), WindowNumber: existing.WindowNumber + 1,
		FirstWindowID: existing.FirstWindowID, PreviousWindowID: &previousWindowID, WindowID: windowID,
		ObservedPrefillTokens: &observed, LastTrigger: WindowTriggerCompletedUsage,
		PromptWindowRevision: promptWindowRevision,
	}
}

func NextEstimatedWindow(conversationID string, promptWindowRevision, estimatedTokens uint64, existing historyruntime.ContextWindowRecord, found bool) historyruntime.ContextWindowRecord {
	estimated := estimatedTokens
	if !found {
		windowID := WindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
		return historyruntime.ContextWindowRecord{
			ConversationID: conversationID, Lane: string(model.PromptLaneRespond), WindowNumber: 1,
			FirstWindowID: windowID, WindowID: windowID, EstimatedPrefillTokens: &estimated,
			LastTrigger: WindowTriggerPreTurnEstimate, PromptWindowRevision: promptWindowRevision,
		}
	}
	if existing.PromptWindowRevision == promptWindowRevision {
		existing.EstimatedPrefillTokens = &estimated
		existing.LastTrigger = WindowTriggerPreTurnEstimate
		return existing
	}
	previousWindowID := existing.WindowID
	windowID := WindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
	return historyruntime.ContextWindowRecord{
		ConversationID: conversationID, Lane: string(model.PromptLaneRespond), WindowNumber: existing.WindowNumber + 1,
		FirstWindowID: existing.FirstWindowID, PreviousWindowID: &previousWindowID, WindowID: windowID,
		EstimatedPrefillTokens: &estimated, LastTrigger: WindowTriggerPreTurnEstimate,
		PromptWindowRevision: promptWindowRevision,
	}
}

func NextCommittedWindow(conversationID string, promptWindowRevision uint64, existing historyruntime.ContextWindowRecord, found bool) historyruntime.ContextWindowRecord {
	windowID := WindowID(conversationID, string(model.PromptLaneRespond), promptWindowRevision)
	if !found {
		return historyruntime.ContextWindowRecord{
			ConversationID: conversationID, Lane: string(model.PromptLaneRespond), WindowNumber: 1,
			FirstWindowID: windowID, WindowID: windowID, LastTrigger: WindowTriggerCompactionCommit,
			PromptWindowRevision: promptWindowRevision,
		}
	}
	if existing.PromptWindowRevision == promptWindowRevision {
		existing.ObservedPrefillTokens = nil
		existing.EstimatedPrefillTokens = nil
		existing.LastTrigger = WindowTriggerCompactionCommit
		existing.FailureCount = 0
		return existing
	}
	previousWindowID := existing.WindowID
	firstWindowID := existing.FirstWindowID
	if firstWindowID == "" {
		firstWindowID = windowID
	}
	return historyruntime.ContextWindowRecord{
		ConversationID: conversationID, Lane: string(model.PromptLaneRespond), WindowNumber: existing.WindowNumber + 1,
		FirstWindowID: firstWindowID, PreviousWindowID: &previousWindowID, WindowID: windowID,
		LastTrigger: WindowTriggerCompactionCommit, PromptWindowRevision: promptWindowRevision,
	}
}

func WindowID(conversationID, lane string, promptWindowRevision uint64) string {
	return character.RuntimeHash(fmt.Sprintf("%s:%s:%d", conversationID, lane, promptWindowRevision))
}

func WindowLedgerMetadata(record *historyruntime.ContextWindowRecord) map[string]any {
	if record == nil {
		return map[string]any{"recorded": false}
	}
	metadata := map[string]any{
		"recorded": true, "lane": record.Lane, "windowNumber": record.WindowNumber,
		"firstWindowIDHash": character.RuntimeHash(record.FirstWindowID),
		"windowIDHash":      character.RuntimeHash(record.WindowID), "lastTrigger": record.LastTrigger,
		"failureCount": record.FailureCount, "promptWindowRevision": record.PromptWindowRevision,
	}
	if record.PreviousWindowID != nil {
		metadata["previousWindowIDHash"] = character.RuntimeHash(*record.PreviousWindowID)
	}
	if record.ObservedPrefillTokens != nil {
		metadata["observedPrefillTokens"] = *record.ObservedPrefillTokens
	}
	if record.EstimatedPrefillTokens != nil {
		metadata["estimatedPrefillTokens"] = *record.EstimatedPrefillTokens
	}
	return metadata
}
