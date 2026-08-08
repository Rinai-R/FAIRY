package session

import "encoding/json"

type SubmitRequest struct {
	Input string `json:"input"`
}

type Outcome struct {
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId"`
	ResponseText   string `json:"responseText"`
}

type SubmitResponse struct {
	Outcome Outcome `json:"outcome"`
}

type Event struct {
	ConversationID string          `json:"conversationId"`
	TurnID         string          `json:"turnId"`
	Sequence       uint64          `json:"sequence"`
	State          string          `json:"state"`
	Payload        json.RawMessage `json:"payload"`
}
