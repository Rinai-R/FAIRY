package conversation

import (
	"context"
	"fairy/agent/tool"
	"fairy/runtime/model"
	"fairy/transport/desktopcapture"
	"fairy/transport/session"
	"time"
)

const (
	desktopToolTimeout       = 20 * time.Second
	desktopToolResultMessage = "A fresh desktop capture is attached. Use only visible evidence from this image; do not infer hidden activity."
)

type DesktopToolRequest = desktopcapture.ToolRequest

type DesktopToolExecution = desktopcapture.Execution

type DesktopToolEvidence = desktopcapture.Evidence

type DesktopToolCoordinator interface {
	Available(conversationID string) bool
	Begin(context.Context, DesktopToolRequest, func()) (DesktopToolExecution, error)
	DispatchExecution(context.Context, DesktopToolExecution) error
	Result(context.Context, string) (DesktopToolEvidence, error)
	CancelTurn(context.Context, string, string) error
}

type DesktopToolError = desktopcapture.ToolError

func AttachDesktopToolCoordinator(service *Service, coordinator DesktopToolCoordinator) {
	if service == nil {
		return
	}
	service.desktopTool = coordinator
}

func desktopToolAllowed(visionInput bool, resolved session.Resolved, coordinator DesktopToolCoordinator, conversationID string) bool {
	return visionInput && resolved.Endpoint == session.EndpointDesktop && resolved.Facts.Audience == session.AudienceSingle && resolved.AllowsPersonalMemory() && coordinator != nil && coordinator.Available(conversationID)
}

func desktopToolPromptItems(callID, arguments string, evidence DesktopToolEvidence) []model.PromptItem {
	parts := model.PromptContentParts{
		{Type: model.PromptContentText, Text: desktopToolResultMessage},
		{Type: model.PromptContentImage, ImageDataURL: evidence.DataURL, ImageMIME: evidence.MediaType, ImagePurpose: "desktop_observation"},
	}
	return []model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: callID, ToolName: tool.DesktopObserve, ToolArguments: arguments},
		{Type: model.PromptItemToolResult, ToolCallID: callID, Parts: &parts},
	}
}

func desktopToolFailurePromptItems(callID, arguments, code string) []model.PromptItem {
	parts := model.PromptContentParts{{Type: model.PromptContentText, Text: "Desktop capture failed with code " + code + ". Continue without visual evidence and do not claim to see the screen."}}
	return []model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: callID, ToolName: tool.DesktopObserve, ToolArguments: arguments},
		{Type: model.PromptItemToolResult, ToolCallID: callID, Parts: &parts},
	}
}
