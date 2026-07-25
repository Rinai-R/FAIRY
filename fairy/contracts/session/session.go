// Package session owns cross-module session open/list wire facts.
package session

import "fairy/contracts/interaction"

type OpenRequest struct {
	Endpoint    interaction.EndpointKind `json:"endpoint"`
	EndpointKey string                   `json:"endpointKey"`
	Interaction interaction.Context      `json:"interaction"`
}

type OpenResponse struct {
	ConversationID string                   `json:"conversationId"`
	CharacterID    string                   `json:"characterId"`
	MessageCount   int                      `json:"messageCount"`
	Endpoint       interaction.EndpointKind `json:"endpoint"`
}

type MessageRecord struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversationId"`
	TurnID          string `json:"turnId"`
	Sequence        uint64 `json:"sequence"`
	Role            string `json:"role"`
	Content         string `json:"content"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
}

type MessagePage struct {
	Messages           []MessageRecord `json:"messages"`
	NextBeforeSequence *uint64         `json:"nextBeforeSequence,omitempty"`
}

type AmbientObservation struct {
	MessageID       string `json:"messageId"`
	SenderID        string `json:"senderId"`
	SenderName      string `json:"senderName"`
	Text            string `json:"text"`
	DirectedToBot   bool   `json:"directedToBot"`
	IsNew           bool   `json:"isNew"`
	TimestampUnixMS int64  `json:"timestampUnixMs"`
}

type ParticipationRequest struct {
	EvaluationReason string               `json:"evaluationReason"`
	Messages         []AmbientObservation `json:"messages"`
}

type ParticipationResponse struct {
	Action          string  `json:"action"`
	TargetMessageID *string `json:"targetMessageId,omitempty"`
	WaitSeconds     *int    `json:"waitSeconds,omitempty"`
}
