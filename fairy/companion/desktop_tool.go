package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"fairy/desktopcapture"
	"fairy/model"
	"fairy/session"
)

const (
	toolDesktopObserve       = "desktop_observe"
	desktopToolTimeout       = 20 * time.Second
	desktopToolResultMessage = "A fresh desktop capture is attached. Use only visible evidence from this image; do not infer hidden activity."
)

type DesktopToolRequest = desktopcapture.ToolRequest

type DesktopToolEvidence = desktopcapture.Evidence

type DesktopToolCoordinator interface {
	Available(conversationID string) bool
	Observe(context.Context, DesktopToolRequest) (DesktopToolEvidence, error)
	CancelTurn(context.Context, string, string) error
}

type DesktopToolError = desktopcapture.ToolError

func AttachDesktopToolCoordinator(service *CompanionService, coordinator DesktopToolCoordinator) {
	if service == nil {
		return
	}
	service.desktopTool = coordinator
}

func desktopToolAllowed(visionInput bool, resolved session.Resolved, coordinator DesktopToolCoordinator, conversationID string) bool {
	return visionInput && resolved.Endpoint == session.EndpointDesktop && resolved.Facts.Audience == session.AudienceSingle && resolved.AllowsPersonalMemory() && coordinator != nil && coordinator.Available(conversationID)
}

func desktopToolSpec() model.ToolSpec {
	return model.ToolSpec{
		Name:        toolDesktopObserve,
		Description: "Capture the current main desktop display once when the user's request cannot be answered reliably without seeing what is visibly on screen. Call at most once in a turn. Do not use for general curiosity.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func validateDesktopToolArguments(arguments string) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &value); err != nil {
		return errors.New("desktop observation arguments must be an empty JSON object")
	}
	if len(value) != 0 {
		return errors.New("desktop observation arguments must be empty")
	}
	return nil
}

func desktopToolPromptItems(callID, arguments string, evidence DesktopToolEvidence) []model.PromptItem {
	parts := model.PromptContentParts{
		{Type: model.PromptContentText, Text: desktopToolResultMessage},
		{Type: model.PromptContentImage, ImageDataURL: evidence.DataURL, ImageMIME: evidence.MediaType, ImagePurpose: "desktop_observation"},
	}
	return []model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: callID, ToolName: toolDesktopObserve, ToolArguments: arguments},
		{Type: model.PromptItemToolResult, ToolCallID: callID, Parts: &parts},
	}
}

func desktopToolFailurePromptItems(callID, arguments, code string) []model.PromptItem {
	parts := model.PromptContentParts{{Type: model.PromptContentText, Text: "Desktop capture failed with code " + code + ". Continue without visual evidence and do not claim to see the screen."}}
	return []model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: callID, ToolName: toolDesktopObserve, ToolArguments: arguments},
		{Type: model.PromptItemToolResult, ToolCallID: callID, Parts: &parts},
	}
}
