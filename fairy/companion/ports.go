package companion

import (
	"context"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/memory/semantic"
	"fairy/model"
	"fairy/profile"
)

// MemoryPort is the persistence + retrieval surface Companion needs from the memory domain.
// Implemented by *memory.Store (service+store merged in the memory package).
type MemoryPort interface {
	LoadConversation(conversationID string) (memory.ConversationBootstrap, error)
	LookupSurfaceForConversation(conversationID string) (string, bool, error)
	BeginTurn(conversationID string, userMessage string) (memory.PersistedTurn, error)
	CompleteTurn(conversationID string, turnID string, assistantMessage string) (memory.MessageRecord, error)
	InterruptTurn(conversationID string, turnID string, publishedPrefix string) (*memory.MessageRecord, error)
	FailTurn(conversationID string, turnID string, code string, message string, retryable bool) error
	CommitPromptWindow(conversationID string, expectedRevision uint64, summary string) (memory.CompactionResult, error)
	Retrieve(characterID string, query string) (memory.RetrievalContext, error)
	RetrieveWithSemanticVectorIndex(context.Context, string, string, semantic.Embedder, memory.SemanticVectorIndex) (memory.RetrievalContext, error)
	RetrievePublicKnowledgeContext(context.Context, string) (memory.RetrievalContext, error)
	AppendTurnRuntimeEvent(input memory.TurnRuntimeEventInput) (memory.TurnRuntimeEventRecord, error)
	SaveLaneContinuation(record memory.LaneContinuationRecord) (memory.LaneContinuationRecord, error)
	LoadLaneContinuation(conversationID string, lane string) (memory.LaneContinuationRecord, bool, error)
	ClearLaneContinuation(conversationID string, lane string) error
	SaveContextWindow(record memory.ContextWindowRecord) (memory.ContextWindowRecord, error)
	LoadContextWindow(conversationID string, lane string) (memory.ContextWindowRecord, bool, error)
	PendingExtractionTurnCount(conversationID string) (uint64, error)
	ClaimExtractionBatch(conversationID string, limit int) (*memory.ExtractionBatchInput, error)
	FailExtractionBatch(batchID, code, message string, retryable bool) error
	CommitMemoryMutations(batchID string, characterID string, allowedMemoryIDs []string, mutations []memory.MemoryMutation) ([]memory.MemoryMutationResult, error)
	ProcessEmbeddingJobsWithVectorIndex(context.Context, semantic.Embedder, memory.VectorIndex, int) (memory.EmbeddingJobResult, error)
	EnqueueKnowledgeIngestSnapshots(snapshots []memory.KnowledgeIngestSnapshot) error
	ProcessKnowledgeIngestJobs(limit int) (int, error)
}

// ModelPort is the model-execution surface Companion needs.
// Implemented by *model.ModelService.
type ModelPort interface {
	ExecuteRequestContext(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error)
	ExecutePrompt(lane model.PromptLane, instructions string, maxOutputTokens uint32, input []model.PromptItem, promptCacheKey string) ([]model.StreamEvent, error)
}

// CharacterCatalog lists character records for persona + visual states.
// Implemented by *character.Store.
type CharacterCatalog interface {
	List() (character.Catalog, error)
}

// ProfileSource reads the current user profile snapshot.
// Implemented by *profile.Store.
type ProfileSource interface {
	Current() (*profile.Snapshot, error)
}

// ConfigSource reads durable model and web-search settings.
// Implemented by *config.Reader.
type ConfigSource interface {
	ModelConnection() (config.ModelConnection, error)
	WebSearchSettings() (config.WebSearchSettings, error)
}

// Compile-time assertions that domain stores satisfy companion ports.
var (
	_ MemoryPort       = (*memory.Store)(nil)
	_ ModelPort        = (*model.ModelService)(nil)
	_ CharacterCatalog = (*character.Store)(nil)
	_ ProfileSource    = (*profile.Store)(nil)
	_ ConfigSource     = (*config.Reader)(nil)
)
