package memory

import (
	"context"

	domainmemory "fairy/internal/domain/memory"
)

// ConversationRepository is the persistence port for conversation turns and windows.
type ConversationRepository interface {
	LoadConversation(ctx context.Context, conversationID string) (domainmemory.ConversationBootstrap, error)
	BeginTurn(ctx context.Context, conversationID string, userMessage string) (domainmemory.PersistedTurn, error)
	BeginInitiationTurn(ctx context.Context, conversationID string, evidenceIDs []string) (domainmemory.PersistedTurn, error)
	CompleteTurn(ctx context.Context, conversationID string, turnID string, assistantMessage string) (domainmemory.MessageRecord, error)
	InterruptTurn(ctx context.Context, conversationID string, turnID string, publishedPrefix string) (*domainmemory.MessageRecord, error)
	FailTurn(ctx context.Context, conversationID string, turnID string, code string, message string, retryable bool) error
	SaveContextWindow(ctx context.Context, record domainmemory.ContextWindowRecord) (domainmemory.ContextWindowRecord, error)
	LoadContextWindow(ctx context.Context, conversationID string, lane string) (domainmemory.ContextWindowRecord, bool, error)
}

// ExtractionRepository is the persistence port for extraction claim/commit.
type ExtractionRepository interface {
	PendingExtractionTurnCount(ctx context.Context, conversationID string) (uint64, error)
	ClaimExtractionBatch(ctx context.Context, conversationID string, limit int) (*domainmemory.ExtractionBatchInput, error)
	FailExtractionBatch(ctx context.Context, batchID, code, message string, retryable bool) error
	CommitMemoryMutations(ctx context.Context, batchID string, characterID string, allowedMemoryIDs []string, mutations []domainmemory.MemoryMutation) ([]domainmemory.MemoryMutationResult, error)
}

// PersonalMemoryRepository is the persistence port for personal memory catalogs.
type PersonalMemoryRepository interface {
	PersonalMemoryCatalog(ctx context.Context, characterID string) (domainmemory.PersonalMemoryCatalog, error)
	CreatePersonalMemory(ctx context.Context, kind string, scope domainmemory.MemoryScope, content string, confidence uint16) (domainmemory.PersonalMemoryRecord, error)
	RevisePersonalMemory(ctx context.Context, id string, content string, confidence uint16) (domainmemory.PersonalMemoryRecord, error)
	TombstonePersonalMemory(ctx context.Context, id string) error
}
