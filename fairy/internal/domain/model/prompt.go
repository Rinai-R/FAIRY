package model

// Package model owns pure model-facing domain facts used by app workflows.
// Provider transport and SDK adapters live outside this package.
type PromptLane string

const (
	PromptLaneRespond        PromptLane = "respond"
	PromptLaneParticipate    PromptLane = "participate"
	PromptLaneCompact        PromptLane = "compact"
	PromptLaneExtract        PromptLane = "extract"
	PromptLaneTranslate      PromptLane = "translate"
	PromptLaneSocialLearn    PromptLane = "social_learn"
	PromptLaneSocialFeedback PromptLane = "social_feedback"
)

type PromptItemType string

const (
	PromptItemUserMessage      PromptItemType = "user_message"
	PromptItemAssistantMessage PromptItemType = "assistant_message"
	PromptItemContextData      PromptItemType = "context_data"
)

type ModelRequestShape struct {
	Lane            PromptLane `json:"lane"`
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions"`
	MaxOutputTokens uint32     `json:"maxOutputTokens"`
	PromptCacheKey  string     `json:"promptCacheKey,omitempty"`
}

type PromptItem struct {
	Type    PromptItemType `json:"type"`
	Content string         `json:"content"`
}
