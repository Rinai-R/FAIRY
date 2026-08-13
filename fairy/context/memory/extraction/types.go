// Package extraction defines the asynchronous memory-learning batch model.
// It is intentionally separate from manual personal-memory curation.
package extraction

import "fairy/context/memory/personal"

const (
	DefaultBatchLimit = 12
	MaxMutations      = 16
	OperationAdd      = "ADD"
	OperationReplace  = "REPLACE"
	OperationDelete   = "DELETE"
	OperationNone     = "NONE"
)

type Turn struct {
	TurnID           string `json:"turnId"`
	UserMessage      string `json:"userMessage"`
	AssistantMessage string `json:"assistantMessage"`
}

type BatchInput struct {
	BatchID          string               `json:"batchId"`
	ConversationID   string               `json:"conversationId"`
	CharacterID      string               `json:"characterId"`
	Turns            []Turn               `json:"turns"`
	ExistingMemories []personal.Retrieved `json:"existingMemories"`
}

// ClaimedBatch is the durable queue claim produced by the SeekDB coordinator.
// Existing memories are deliberately absent: 4.2 enriches this claim from the
// SeekDB personal-memory authority before atomically settling the batch.
type ClaimedBatch struct {
	BatchID        string `json:"batchId"`
	ConversationID string `json:"conversationId"`
	CharacterID    string `json:"characterId"`
	Turns          []Turn `json:"turns"`
}

type Mutation struct {
	Operation             string         `json:"operation"`
	SourceTurnID          string         `json:"sourceTurnId"`
	MemoryID              string         `json:"memoryId,omitempty"`
	Kind                  string         `json:"kind"`
	Scope                 personal.Scope `json:"scope"`
	Content               string         `json:"content"`
	ConfidenceBasisPoints uint16         `json:"confidenceBasisPoints"`
}

type MutationResult struct {
	Status           string `json:"status"`
	MemoryID         string `json:"memoryId,omitempty"`
	ExistingMemoryID string `json:"existingMemoryId,omitempty"`
}

type MutationOutput struct {
	Mutations []Mutation `json:"mutations"`
}

type WireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type BatchRecord struct {
	ID                string     `json:"id"`
	ConversationID    string     `json:"conversationId"`
	CharacterID       string     `json:"characterId"`
	Status            string     `json:"status"`
	FirstTurnSequence uint64     `json:"firstTurnSequence"`
	LastTurnSequence  uint64     `json:"lastTurnSequence"`
	Error             *WireError `json:"error"`
	CreatedAtUnixMS   int64      `json:"createdAtUnixMs"`
	UpdatedAtUnixMS   int64      `json:"updatedAtUnixMs"`
}

type Catalog struct {
	Running []BatchRecord `json:"running"`
	Failed  []BatchRecord `json:"failed"`
}

type Coverage struct {
	ConversationID       string `json:"conversationId"`
	TurnID               string `json:"turnId"`
	MemoryID             string `json:"memoryId"`
	ResultStatus         string `json:"resultStatus"`
	TurnSequence         uint64 `json:"turnSequence,omitempty"`
	StartMessageSequence uint64 `json:"startMessageSequence,omitempty"`
	EndMessageSequence   uint64 `json:"endMessageSequence,omitempty"`
	CoveredTokens        uint64 `json:"coveredTokens,omitempty"`
	CreatedAtUnixMS      int64  `json:"createdAtUnixMs"`
}
