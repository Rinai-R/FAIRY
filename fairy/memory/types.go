package memory

// Package memory owns memory domain facts, validation, projections,
// PostgreSQL persistence, and workers.
const (
	DefaultExtractionBatchLimit       = 12
	MaxMemoryMutationsPerBatch        = 16
	MaxPersonalMemoryContentRunes     = 2400
	MaxFTSQueryChars                  = 2000
	extractionProjectionFragmentRunes = 64
)

type ConversationRecord struct {
	ID              string `json:"id"`
	CharacterID     string `json:"characterId"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
	UpdatedAtUnixMS int64  `json:"updatedAtUnixMs"`
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

type PromptWindowRecord struct {
	ConversationID        string  `json:"conversationId"`
	Revision              uint64  `json:"revision"`
	Summary               *string `json:"summary"`
	CutoffMessageSequence uint64  `json:"cutoffMessageSequence"`
	UpdatedAtUnixMS       int64   `json:"updatedAtUnixMs"`
}

type ConversationBootstrap struct {
	Conversation ConversationRecord `json:"conversation"`
	Messages     []MessageRecord    `json:"messages"`
	PromptWindow PromptWindowRecord `json:"promptWindow"`
}

// ConversationPromptContext is the active dialogue projection used to build
// model prompts. Messages contains only rows after the PromptWindow cutoff.
type ConversationPromptContext struct {
	Conversation ConversationRecord `json:"conversation"`
	Messages     []MessageRecord    `json:"messages"`
	PromptWindow PromptWindowRecord `json:"promptWindow"`
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

type MemoryScope struct {
	Type        string `json:"type"`
	CharacterID string `json:"characterId,omitempty"`
}

type PersonalMemoryRecord struct {
	ID                    string      `json:"id"`
	Kind                  string      `json:"kind"`
	Scope                 MemoryScope `json:"scope"`
	ReviewStatus          string      `json:"reviewStatus"`
	Content               string      `json:"content"`
	Status                string      `json:"status"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
	SourceConversationID  string      `json:"sourceConversationId"`
	SourceTurnID          string      `json:"sourceTurnId"`
	SupersedesID          *string     `json:"supersedesId"`
	CreatedAtUnixMS       int64       `json:"createdAtUnixMs"`
	UpdatedAtUnixMS       int64       `json:"updatedAtUnixMs"`
}

type PersonalMemoryCatalog struct {
	Global      []PersonalMemoryRecord `json:"global"`
	Character   []PersonalMemoryRecord `json:"character"`
	NeedsReview []PersonalMemoryRecord `json:"needsReview"`
}

type RetrievedPersonalMemory struct {
	ID                    string      `json:"id"`
	Kind                  string      `json:"kind"`
	Layer                 string      `json:"layer"`
	Scope                 MemoryScope `json:"scope"`
	Content               string      `json:"content"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
	UpdatedAtUnixMS       int64       `json:"updatedAtUnixMs"`
}

type ExtractionTurn struct {
	TurnID           string `json:"turnId"`
	UserMessage      string `json:"userMessage"`
	AssistantMessage string `json:"assistantMessage"`
}

type ExtractionBatchInput struct {
	BatchID          string                    `json:"batchId"`
	ConversationID   string                    `json:"conversationId"`
	CharacterID      string                    `json:"characterId"`
	Turns            []ExtractionTurn          `json:"turns"`
	ExistingMemories []RetrievedPersonalMemory `json:"existingMemories"`
}

type MemoryMutation struct {
	Operation             string      `json:"operation"`
	SourceTurnID          string      `json:"sourceTurnId"`
	MemoryID              string      `json:"memoryId,omitempty"`
	Kind                  string      `json:"kind"`
	Scope                 MemoryScope `json:"scope"`
	Content               string      `json:"content"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
}

type MemoryMutationResult struct {
	Status           string `json:"status"`
	MemoryID         string `json:"memoryId,omitempty"`
	ExistingMemoryID string `json:"existingMemoryId,omitempty"`
}

type MemoryMutationOutput struct {
	Mutations []MemoryMutation `json:"mutations"`
}

type ContextWindowRecord struct {
	ConversationID         string  `json:"conversationId"`
	Lane                   string  `json:"lane"`
	WindowNumber           uint64  `json:"windowNumber"`
	FirstWindowID          string  `json:"firstWindowId"`
	PreviousWindowID       *string `json:"previousWindowId,omitempty"`
	WindowID               string  `json:"windowId"`
	ObservedPrefillTokens  *uint64 `json:"observedPrefillTokens,omitempty"`
	EstimatedPrefillTokens *uint64 `json:"estimatedPrefillTokens,omitempty"`
	LastTrigger            string  `json:"lastTrigger"`
	FailureCount           uint64  `json:"failureCount"`
	PromptWindowRevision   uint64  `json:"promptWindowRevision"`
	UpdatedAtUnixMS        int64   `json:"updatedAtUnixMs"`
}

type CompactionResult struct {
	WindowRevision        uint64 `json:"windowRevision"`
	RetainedDialogueItems int    `json:"retainedDialogueItems"`
}
