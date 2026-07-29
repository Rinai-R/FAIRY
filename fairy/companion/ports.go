package companion

import (
	"context"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"

	"fairy/session"
)

type InteractionBindingStore interface {
	LookupEndpointForConversation(conversationID string) (session.Binding, bool, error)
}

type ConversationActivityStore interface {
	LoadConversationActivity(conversationID string, nowUnixMS int64) (memory.ConversationActivity, error)
}

type ConversationMetadataStore interface {
	LoadConversationRecord(conversationID string) (memory.ConversationRecord, error)
}

// PromptContextStore reads only the active dialogue required to build model
// prompts. Full conversation history is not part of Companion's production ports.
type PromptContextStore interface {
	LoadConversationPrompt(conversationID string) (memory.ConversationPromptContext, error)
}

// TurnStore owns the durable turn lifecycle writes consumed by TurnEngine.
type TurnStore interface {
	BeginTurn(conversationID string, userMessage string) (memory.PersistedTurn, error)
	BeginInitiationTurn(conversationID string, evidenceIDs []string) (memory.PersistedTurn, error)
	CompleteExpressionTurn(conversationID string, turnID string, assistantMessage string, parts []memory.ExpressionPart) (memory.MessageRecord, error)
	InterruptExpressionTurn(conversationID string, turnID string, publishedPrefix string, parts []memory.ExpressionPart) (*memory.MessageRecord, error)
	FailTurn(conversationID string, turnID string, code string, message string, retryable bool) error
}

// MemoryRetrievalStore is the model-tool read surface for private and verified
// public memory.
type MemoryRetrievalStore interface {
	Retrieve(characterID string, query string) (memory.RetrievalContext, error)
	RetrievePublicKnowledgeContext(context.Context, string) (memory.RetrievalContext, error)
	RetrieveCharacterSocialMemoryContext(context.Context, string, string) (memory.SocialMemoryContext, error)
}

// PortraitStore reads the bounded private companion portrait projection.
type PortraitStore interface {
	CompanionPortraitContext(context.Context, string) (memory.RetrievalContext, error)
	RetrieveCharacterSocialMemoryContext(context.Context, string, string) (memory.SocialMemoryContext, error)
}

// SocialRetrievalStore reads public social context within one conversation.
type SocialRetrievalStore interface {
	RetrieveSocialMemoryContext(context.Context, string, string, string) (memory.SocialMemoryContext, error)
}

// RuntimeStateStore owns prompt continuation, context-window, compaction, and
// privacy-safe runtime ledger state.
type RuntimeStateStore interface {
	CommitCompaction(conversationID string, expectedRevision uint64, summary string, contextWindow memory.ContextWindowRecord, clearLane string) (memory.CompactionResult, error)
	AppendTurnRuntimeEvent(input memory.TurnRuntimeEventInput) (memory.TurnRuntimeEventRecord, error)
	SaveLaneContinuation(record memory.LaneContinuationRecord) (memory.LaneContinuationRecord, error)
	LoadLaneContinuation(conversationID string, lane string) (memory.LaneContinuationRecord, bool, error)
	ClearLaneContinuation(conversationID string, lane string) error
	SaveContextWindow(record memory.ContextWindowRecord) (memory.ContextWindowRecord, error)
	LoadContextWindow(conversationID string, lane string) (memory.ContextWindowRecord, bool, error)
}

// extractionStore owns bounded extraction jobs coordinated after a turn.
type extractionStore interface {
	PendingExtractionTurnCount(conversationID string) (uint64, error)
	ClaimExtractionBatch(conversationID string, limit int) (*memory.ExtractionBatchInput, error)
	FailExtractionBatch(batchID, code, message string, retryable bool) error
	CommitMemoryMutations(batchID string, characterID string, allowedMemoryIDs []string, mutations []memory.MemoryMutation) ([]memory.MemoryMutationResult, error)
}

// knowledgeIngestStore owns the independent verified-knowledge ingest queue.
type knowledgeIngestStore interface {
	EnqueueKnowledgeIngestBatches(batches []memory.KnowledgeIngestBatch) error
	ClaimKnowledgeIngestBatches(limit int) ([]memory.KnowledgeIngestClaim, error)
	CommitKnowledgeIngestBatch(jobID, batchID string, facts []memory.KnowledgeIngestFact) (int, error)
	KnowledgeDocumentsNeedExtraction(jobID, batchID string, documents []memory.KnowledgeDocument) (bool, error)
	CommitKnowledgeDocumentBatch(jobID, batchID string, documents []memory.KnowledgeDocument, facts []memory.KnowledgeIngestFact) (int, error)
	FailKnowledgeIngestBatch(jobID, message string) error
	RetryKnowledgeIngestBatch(jobID, category, message string) error
	DropKnowledgeIngestBatch(jobID, message string) error
}

// SocialContextStore reads public feedback and person-note context.
type SocialContextStore interface {
	RecentSocialFeedbackSummary(context.Context, string, string) (memory.RecentSocialFeedbackSummary, error)
	ListSocialPersonNotes(context.Context, string, string, []string) ([]memory.SocialPersonNote, error)
}

// SocialLearningStore owns public learning, feedback, and person-note writes.
type SocialLearningStore interface {
	StoreSocialMemoryEntries(context.Context, memory.SocialMemoryBatchInput) ([]memory.SocialMemoryEntry, error)
	RecordSocialReplyFeedback(context.Context, memory.SocialReplyFeedbackInput) (memory.SocialReplyFeedback, error)
	UpsertSocialPersonNote(context.Context, memory.SocialPersonNoteInput) (memory.SocialPersonNote, error)
}

type turnMemoryPorts struct {
	promptContext   PromptContextStore
	turns           TurnStore
	memoryRetrieval MemoryRetrievalStore
	portrait        PortraitStore
	runtimeState    RuntimeStateStore
}

type ambientMemoryPorts struct {
	bindings        InteractionBindingStore
	activity        ConversationActivityStore
	metadata        ConversationMetadataStore
	socialRetrieval SocialRetrievalStore
	socialContext   SocialContextStore
	socialLearning  SocialLearningStore
}

type retentionMemoryPorts struct {
	extraction extractionStore
	knowledge  knowledgeIngestStore
}

type memoryPorts struct {
	turn      turnMemoryPorts
	ambient   ambientMemoryPorts
	retention retentionMemoryPorts
}

func memoryPortsFromStore(store *memory.Store) memoryPorts {
	if store == nil {
		return memoryPorts{}
	}
	return memoryPorts{
		turn: turnMemoryPorts{
			promptContext:   store,
			turns:           store,
			memoryRetrieval: store,
			portrait:        store,
			runtimeState:    store,
		},
		ambient: ambientMemoryPorts{
			bindings:        store,
			activity:        store,
			metadata:        store,
			socialRetrieval: store,
			socialContext:   store,
			socialLearning:  store,
		},
		retention: retentionMemoryPorts{extraction: store, knowledge: store},
	}
}

func (p memoryPorts) ready() bool {
	return p.turn.promptContext != nil &&
		p.turn.turns != nil &&
		p.turn.memoryRetrieval != nil &&
		p.turn.portrait != nil &&
		p.turn.runtimeState != nil &&
		p.ambient.bindings != nil &&
		p.ambient.activity != nil &&
		p.ambient.metadata != nil &&
		p.ambient.socialRetrieval != nil &&
		p.ambient.socialContext != nil &&
		p.ambient.socialLearning != nil &&
		p.retention.extraction != nil &&
		p.retention.knowledge != nil
}

type OwnerIdentityPort interface {
	IsOwner(namespace, principalDigest string) (bool, error)
}

// ModelPort is the model-execution surface Companion needs.
// Implemented by *model.ModelService.
type ModelPort interface {
	ExecuteRequestContext(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error)
	ExecutePrompt(lane model.PromptLane, instructions string, maxOutputTokens uint32, input []model.PromptItem, promptCacheKey string) ([]model.StreamEvent, error)
}

// StreamingModelPort is an optional extension implemented by model services
// that can deliver provider events before the request completes.
type StreamingModelPort interface {
	ExecuteRequestContextStream(ctx context.Context, request model.CompiledPromptRequest, onEvent func(model.StreamEvent)) error
}

// CharacterLookup reads the one conversation character needed for persona and
// visual states without enumerating the management catalog.
// Implemented by *character.Store.
type CharacterLookup interface {
	Lookup(characterID string) (character.Record, bool, error)
}

// ProfileSource reads the current user profile snapshot.
// Implemented by *config.ProfileStore.
type ProfileSource interface {
	Current() (*config.ProfileSnapshot, error)
}

// ConfigSource reads durable model and web-search settings.
// Implemented by *config.Reader.
type ConfigSource interface {
	ModelConnection() (config.ModelConnection, error)
	WebSearchSettings() (config.WebSearchSettings, error)
}

// Compile-time assertions that domain stores satisfy companion ports.
var (
	_ InteractionBindingStore   = (*memory.Store)(nil)
	_ ConversationActivityStore = (*memory.Store)(nil)
	_ ConversationMetadataStore = (*memory.Store)(nil)
	_ PromptContextStore        = (*memory.Store)(nil)
	_ TurnStore                 = (*memory.Store)(nil)
	_ MemoryRetrievalStore      = (*memory.Store)(nil)
	_ PortraitStore             = (*memory.Store)(nil)
	_ SocialRetrievalStore      = (*memory.Store)(nil)
	_ RuntimeStateStore         = (*memory.Store)(nil)
	_ extractionStore           = (*memory.Store)(nil)
	_ knowledgeIngestStore      = (*memory.Store)(nil)
	_ SocialContextStore        = (*memory.Store)(nil)
	_ SocialLearningStore       = (*memory.Store)(nil)
	_ ModelPort                 = (*model.ModelService)(nil)
	_ CharacterLookup           = (*character.Store)(nil)
	_ ProfileSource             = (*config.ProfileStore)(nil)
	_ ConfigSource              = (*config.Reader)(nil)
	_ OwnerIdentityPort         = (*memory.IdentityStore)(nil)
)
