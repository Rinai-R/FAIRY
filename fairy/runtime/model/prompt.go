package model

// Package model owns model requests, provider transport, and normalized events.
type PromptLane string

const (
	PromptLaneRespond            PromptLane = "respond"
	PromptLaneParticipate        PromptLane = "participate"
	PromptLaneCompact            PromptLane = "compact"
	PromptLaneExtract            PromptLane = "extract"
	PromptLaneLearningDiscovery  PromptLane = "learning_discovery"
	PromptLaneSocialLearn        PromptLane = "social_learn"
	PromptLaneSocialFeedback     PromptLane = "social_feedback"
	PromptLaneKnowledgeReconcile PromptLane = "knowledge_reconcile"
)

type PromptItemType string

const (
	PromptItemUserMessage      PromptItemType = "user_message"
	PromptItemAssistantMessage PromptItemType = "assistant_message"
	PromptItemContextData      PromptItemType = "context_data"
	PromptItemToolCall         PromptItemType = "tool_call"
	PromptItemToolResult       PromptItemType = "tool_result"
)

type PromptContentPartType string

const (
	PromptContentText  PromptContentPartType = "text"
	PromptContentImage PromptContentPartType = "image"
)

type PromptContentPart struct {
	Type         PromptContentPartType `json:"type"`
	Text         string                `json:"text,omitempty"`
	ImageDataURL string                `json:"imageDataUrl,omitempty"`
	ImageMIME    string                `json:"imageMime,omitempty"`
	ImagePurpose string                `json:"imagePurpose,omitempty"`
}

type PromptContentParts []PromptContentPart

type ModelRequestShape struct {
	Lane            PromptLane `json:"lane"`
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions"`
	MaxOutputTokens uint32     `json:"maxOutputTokens"`
	PromptCacheKey  string     `json:"promptCacheKey,omitempty"`
}

type PromptItem struct {
	Type          PromptItemType      `json:"type"`
	Content       string              `json:"content,omitempty"`
	Parts         *PromptContentParts `json:"parts,omitempty"`
	ToolCallID    string              `json:"toolCallId,omitempty"`
	ToolName      string              `json:"toolName,omitempty"`
	ToolArguments string              `json:"toolArguments,omitempty"`
}
