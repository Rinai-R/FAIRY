package transcript

import (
	historyexpr "fairy/context/history/expression"
	historyprojection "fairy/context/history/projection"
)

type ConversationRecord struct {
	ID              string `json:"id"`
	CharacterID     string `json:"characterId"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
	UpdatedAtUnixMS int64  `json:"updatedAtUnixMs"`
}

type MessageRecord struct {
	ID              string             `json:"id"`
	MessageID       string             `json:"messageId,omitempty"`
	ConversationID  string             `json:"conversationId"`
	TurnID          string             `json:"turnId"`
	Sequence        uint64             `json:"sequence"`
	Role            string             `json:"role"`
	Content         string             `json:"content"`
	Parts           []historyexpr.Part `json:"parts"`
	CreatedAtUnixMS int64              `json:"createdAtUnixMs"`
}

type PromptWindowRecord struct {
	ConversationID        string                  `json:"conversationId"`
	Revision              uint64                  `json:"revision"`
	Summary               *string                 `json:"summary"`
	CutoffMessageSequence uint64                  `json:"cutoffMessageSequence"`
	ProjectionRevision    uint64                  `json:"projectionRevision"`
	Projection            historyprojection.State `json:"projection"`
	UpdatedAtUnixMS       int64                   `json:"updatedAtUnixMs"`
}

// TranscriptBoundary is the append-only transcript version observed before a
// prompt is materialized. Compaction commits compare both sequences after
// locking the conversation root so a newly-created Turn (including an
// initiation without a message) and a newly-appended message are both visible
// to optimistic concurrency control.
type TranscriptBoundary struct {
	TurnSequence    uint64 `json:"turnSequence"`
	MessageSequence uint64 `json:"messageSequence"`
}

type ConversationBootstrap struct {
	Conversation       ConversationRecord `json:"conversation"`
	Messages           []MessageRecord    `json:"messages"`
	PromptWindow       PromptWindowRecord `json:"promptWindow"`
	TranscriptBoundary TranscriptBoundary `json:"transcriptBoundary"`
}

type ConversationPromptContext struct {
	Conversation       ConversationRecord `json:"conversation"`
	Messages           []MessageRecord    `json:"messages"`
	PromptWindow       PromptWindowRecord `json:"promptWindow"`
	TranscriptBoundary TranscriptBoundary `json:"transcriptBoundary"`
}

type ConversationActivity struct {
	Conversation                 ConversationRecord `json:"conversation"`
	AssistantMessages5Minutes    uint64             `json:"assistantMessages5Minutes"`
	AssistantMessages30Minutes   uint64             `json:"assistantMessages30Minutes"`
	UserMessages30Minutes        uint64             `json:"userMessages30Minutes"`
	LastAssistantMessageAtUnixMS *int64             `json:"lastAssistantMessageAtUnixMs,omitempty"`
}

type PersistedTurn struct {
	ID             string        `json:"id"`
	ConversationID string        `json:"conversationId"`
	UserMessage    MessageRecord `json:"userMessage"`
}

type MessagePage struct {
	Messages           []MessageRecord `json:"messages"`
	NextBeforeSequence *uint64         `json:"nextBeforeSequence,omitempty"`
}

type CompactedTranscriptTurn struct {
	TurnID   string          `json:"turnId"`
	Score    float64         `json:"score"`
	Messages []MessageRecord `json:"messages"`
}

type CompactedTranscriptRecall struct {
	Turns     []CompactedTranscriptTurn `json:"turns"`
	Truncated bool                      `json:"truncated"`
}
