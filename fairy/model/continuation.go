package model

import (
	"errors"
	"strings"
	"unicode"
)

// Continuation types describe how a response continues across model calls.

type ContinuationState struct {
	PreviousResponseID string
	PreviousRequest    CompiledPromptRequest
	ResponseItems      []PromptItem
	ResponseComplete   bool
}

type ContinuationFullRequestReason string

const (
	ContinuationCapabilityUnsupported      ContinuationFullRequestReason = "capability_unsupported"
	ContinuationNoPreviousState            ContinuationFullRequestReason = "no_previous_state"
	ContinuationPreviousResponseIncomplete ContinuationFullRequestReason = "previous_response_incomplete"
	ContinuationRequestShapeChanged        ContinuationFullRequestReason = "request_shape_changed"
	ContinuationPrefixMismatch             ContinuationFullRequestReason = "prefix_mismatch"
	ContinuationInputNotExtended           ContinuationFullRequestReason = "input_not_extended"
)

type ContinuationDecision struct {
	Incremental        bool
	PreviousResponseID string
	NewItems           []PromptItem
	FullReason         ContinuationFullRequestReason
}

func DecideContinuation(continuationSupported bool, previous *ContinuationState, current CompiledPromptRequest) ContinuationDecision {
	if !continuationSupported {
		return fullContinuation(ContinuationCapabilityUnsupported)
	}
	if previous == nil {
		return fullContinuation(ContinuationNoPreviousState)
	}
	if !previous.ResponseComplete || previous.PreviousResponseID == "" {
		return fullContinuation(ContinuationPreviousResponseIncomplete)
	}
	if previous.PreviousRequest.Shape != current.Shape {
		return fullContinuation(ContinuationRequestShapeChanged)
	}
	expectedPrefix := make([]PromptItem, 0, len(previous.PreviousRequest.Input)+len(previous.ResponseItems))
	expectedPrefix = append(expectedPrefix, previous.PreviousRequest.Input...)
	expectedPrefix = append(expectedPrefix, previous.ResponseItems...)
	if !promptItemsHavePrefix(current.Input, expectedPrefix) {
		return fullContinuation(ContinuationPrefixMismatch)
	}
	if len(current.Input) == len(expectedPrefix) {
		return fullContinuation(ContinuationInputNotExtended)
	}
	return ContinuationDecision{
		Incremental:        true,
		PreviousResponseID: previous.PreviousResponseID,
		NewItems:           append([]PromptItem(nil), current.Input[len(expectedPrefix):]...),
	}
}

func MaterializeContinuationRequest(current CompiledPromptRequest, decision ContinuationDecision) (CompiledPromptRequest, error) {
	if decision.Incremental {
		if len(decision.NewItems) == 0 {
			return CompiledPromptRequest{}, errors.New("continuation suffix input 不能为空")
		}
		if err := ValidatePreviousResponseID(decision.PreviousResponseID); err != nil {
			return CompiledPromptRequest{}, err
		}
		current.Input = append([]PromptItem(nil), decision.NewItems...)
		current.PreviousResponseID = decision.PreviousResponseID
		return current, nil
	}
	current.PreviousResponseID = ""
	return current, nil
}

func ValidatePreviousResponseID(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("previous_response_id 必须是非空有效文本")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("previous_response_id 必须是非空有效文本")
		}
	}
	return nil
}

func ResponseIDFromEvents(events []StreamEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "completed" {
			return strings.TrimSpace(events[i].Data)
		}
	}
	return ""
}

func fullContinuation(reason ContinuationFullRequestReason) ContinuationDecision {
	return ContinuationDecision{FullReason: reason}
}

func promptItemsHavePrefix(items []PromptItem, prefix []PromptItem) bool {
	if len(items) < len(prefix) {
		return false
	}
	for i := range prefix {
		if items[i] != prefix[i] {
			return false
		}
	}
	return true
}
