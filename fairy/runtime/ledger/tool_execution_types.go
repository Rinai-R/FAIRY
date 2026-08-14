package ledger

import (
	"errors"
	"fmt"
	"strings"
)

const maxToolRuntimeTokenRunes = 256

type ToolExecutionStatus string

const (
	ToolExecutionPending   ToolExecutionStatus = "pending"
	ToolExecutionCompleted ToolExecutionStatus = "completed"
	ToolExecutionFailed    ToolExecutionStatus = "failed"
	ToolExecutionCancelled ToolExecutionStatus = "cancelled"

	ToolNameDesktopObserve = "desktop_observe"
	MaxToolEvidenceBytes   = 768 << 10
)

type ToolExecutionRecord struct {
	ID                     string              `json:"id"`
	ConversationID         string              `json:"conversationId"`
	TurnID                 string              `json:"turnId"`
	CallID                 string              `json:"callId"`
	ToolName               string              `json:"toolName"`
	Status                 ToolExecutionStatus `json:"status"`
	DeadlineAtUnixMS       int64               `json:"deadlineAtUnixMs"`
	AttemptCount           int                 `json:"attemptCount"`
	LastDispatchedAtUnixMS *int64              `json:"lastDispatchedAtUnixMs,omitempty"`
	ErrorCode              *string             `json:"errorCode,omitempty"`
	ErrorMessage           *string             `json:"errorMessage,omitempty"`
	ResultMediaType        *string             `json:"resultMediaType,omitempty"`
	ResultWidth            *int                `json:"resultWidth,omitempty"`
	ResultHeight           *int                `json:"resultHeight,omitempty"`
	ResultByteCount        *int                `json:"resultByteCount,omitempty"`
	ResultSHA256           *string             `json:"resultSha256,omitempty"`
	CreatedAtUnixMS        int64               `json:"createdAtUnixMs"`
	UpdatedAtUnixMS        int64               `json:"updatedAtUnixMs"`
}

type CreateToolExecutionInput struct {
	ConversationID   string
	TurnID           string
	CallID           string
	ToolName         string
	DeadlineAtUnixMS int64
}

type CompleteToolExecutionInput struct {
	ID              string
	ConversationID  string
	TurnID          string
	CallID          string
	ResultMediaType string
	ResultWidth     int
	ResultHeight    int
	ResultByteCount int
	ResultSHA256    string
}

func validateToolHash(label, value string) error {
	if len(value) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", label)
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return fmt.Errorf("%s must be lowercase sha256 hex", label)
	}
	return nil
}

func validateToolRuntimeToken(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || containsDisallowedControl(value) || len([]rune(value)) > maxToolRuntimeTokenRunes {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func validateCreateToolExecution(input CreateToolExecutionInput, now int64) error {
	if err := validateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := validateID("turn_id", input.TurnID); err != nil {
		return err
	}
	if err := validateID("call_id", input.CallID); err != nil {
		return err
	}
	if input.ToolName != ToolNameDesktopObserve {
		return errors.New("tool name is unsupported")
	}
	if input.DeadlineAtUnixMS <= now {
		return errors.New("tool execution deadline must be in the future")
	}
	return nil
}

func validateCompleteToolExecution(input CompleteToolExecutionInput) error {
	for label, value := range map[string]string{
		"execution_id": input.ID, "conversation_id": input.ConversationID,
		"turn_id": input.TurnID, "call_id": input.CallID,
	} {
		if err := validateID(label, value); err != nil {
			return err
		}
	}
	if input.ResultMediaType != "image/png" && input.ResultMediaType != "image/jpeg" {
		return errors.New("tool result media type is unsupported")
	}
	if input.ResultWidth <= 0 || input.ResultHeight <= 0 {
		return errors.New("tool result dimensions are invalid")
	}
	if input.ResultByteCount <= 0 || input.ResultByteCount > MaxToolEvidenceBytes {
		return errors.New("tool result byte count is invalid")
	}
	if err := validateToolHash("result_sha256", input.ResultSHA256); err != nil {
		return err
	}
	return nil
}

func validateToolExecutionFailure(code, message string) error {
	if err := validateToolRuntimeToken("tool execution error code", code); err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" || strings.TrimSpace(message) != message || containsDisallowedControl(message) || len([]rune(message)) > 512 {
		return errors.New("tool execution error message is invalid")
	}
	return nil
}
