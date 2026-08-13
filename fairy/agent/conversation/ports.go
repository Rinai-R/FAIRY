package conversation

import (
	"context"

	"fairy/context/character"
	historycompaction "fairy/context/history/compaction"
	historyexpr "fairy/context/history/expression"
	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	history "fairy/context/history/transcript"
	"fairy/context/identity"
	"fairy/context/knowledge"
	"fairy/context/memory/extraction"
	"fairy/context/memory/personal"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/model"

	"fairy/transport/session"
)

type InteractionBindingStore interface {
	LookupEndpointForConversation(conversationID string) (session.Binding, bool, error)
}

type ConversationActivityStore interface {
	LoadConversationActivity(conversationID string, nowUnixMS int64) (history.ConversationActivity, error)
}

type ConversationMetadataStore interface {
	LoadConversationRecord(conversationID string) (history.ConversationRecord, error)
}

// PromptContextStore reads only the active dialogue required to build model
// prompts. Full conversation history is not part of Companion's production ports.
type PromptContextStore interface {
	LoadConversationPrompt(conversationID string) (history.ConversationPromptContext, error)
}

// CompactedTranscriptRecallStore reads exact older dialogue only within the
// current conversation and only up to the trusted prompt-window cutoff.
type CompactedTranscriptRecallStore interface {
	SearchCompactedTranscript(context.Context, string, uint64, string, int) (history.CompactedTranscriptRecall, error)
}

// TurnStore owns the durable turn lifecycle writes consumed by TurnEngine.
type TurnStore interface {
	BeginCorrelatedTurn(conversationID string, userMessage string, messageID string) (history.PersistedTurn, error)
	BeginInitiationTurn(conversationID string, evidenceIDs []string) (history.PersistedTurn, error)
	CompleteExpressionTurnForPolicy(conversationID string, turnID string, assistantMessage string, parts []historyexpr.Part, extractionEligible bool) (history.MessageRecord, error)
	InterruptExpressionTurn(conversationID string, turnID string, publishedPrefix string, parts []historyexpr.Part) (*history.MessageRecord, error)
	FailTurn(conversationID string, turnID string, code string, message string, retryable bool) error
}

// MemoryRetrievalStore is the model-tool read surface for private memory.
type MemoryRetrievalStore interface {
	Retrieve(characterID string, query string) (personal.Retrieval, error)
}

type KnowledgeRetrievalStore interface {
	RetrieveContext(context.Context, string) (knowledge.Retrieval, error)
}

// PortraitStore reads the bounded private companion portrait projection.
type PortraitStore interface {
	CompanionPortraitContext(context.Context, string) (personal.Retrieval, error)
}

// SocialRetrievalStore reads public social context within one conversation.
type SocialRetrievalStore interface {
	RetrieveSocialMemoryContext(context.Context, string, string, string) (social.SocialMemoryContext, error)
	RetrieveCharacterSocialMemoryContext(context.Context, string, string) (social.SocialMemoryContext, error)
}

// RuntimeStateStore owns prompt continuation, context-window, compaction, and
// privacy-safe runtime ledger state.
type RuntimeStateStore interface {
	AppendTurnRuntimeEvent(input historyruntime.TurnRuntimeEventInput) (historyruntime.TurnRuntimeEventRecord, error)
	SaveLaneContinuation(record historyruntime.LaneContinuationRecord) (historyruntime.LaneContinuationRecord, error)
	LoadLaneContinuation(conversationID string, lane string) (historyruntime.LaneContinuationRecord, bool, error)
	ClearLaneContinuation(conversationID string, lane string) error
	SaveContextWindow(record historyruntime.ContextWindowRecord) (historyruntime.ContextWindowRecord, error)
	LoadContextWindow(conversationID string, lane string) (historyruntime.ContextWindowRecord, bool, error)
}

type ContextRetentionStore interface {
	CommitCompaction(conversationID string, expectedRevision uint64, expectedTranscript history.TranscriptBoundary, summary string, contextWindow historyruntime.ContextWindowRecord, clearLane string) (historycompaction.Result, error)
	CommitTieredCompaction(conversationID string, expectedWindowRevision uint64, expectedProjectionRevision uint64, expectedTranscript history.TranscriptBoundary, summary string, cutoff uint64, projection historyprojection.State, contextWindow historyruntime.ContextWindowRecord, clearLane string) (historycompaction.Result, error)
	CommitPromptProjection(conversationID string, expectedWindowRevision uint64, expectedProjectionRevision uint64, expectedTranscript history.TranscriptBoundary, projection historyprojection.State, contextWindow historyruntime.ContextWindowRecord, clearLane string) (historycompaction.Result, error)
}

type MemoryCoverageStore interface {
	LoadCommittedMemoryCoverage(conversationID string) ([]extraction.Coverage, error)
}

// extractionStore owns bounded extraction jobs coordinated after a turn.
type extractionStore interface {
	PendingExtractionTurnCount(conversationID string) (uint64, error)
	ClaimExtractionBatch(conversationID string, limit int) (*extraction.BatchInput, error)
	FailExtractionBatch(batchID, code, message string, retryable bool) error
	CommitClaimedMemoryMutationsContext(context.Context, *extraction.BatchInput, []extraction.Mutation) ([]extraction.MutationResult, error)
}

// knowledgeIngestStore owns verified-knowledge retrieval and direct writes.
type knowledgeIngestStore interface {
	SearchKnowledgeForIngestContext(context.Context, string, int) ([]knowledge.Retrieved, error)
	CommitKnowledgeDocumentActionsContext(context.Context, knowledge.IngestTask, knowledge.Document, []string, []knowledge.DocumentAction) (int, error)
}

// SocialContextStore reads public feedback and person-note context.
type SocialContextStore interface {
	ListSocialPersonNotes(context.Context, string, string, []string) ([]social.SocialPersonNote, error)
}

// SocialLearningStore owns public learning, feedback, and person-note writes.
type SocialLearningStore interface {
	StoreSocialMemoryEntries(context.Context, social.SocialMemoryBatchInput) ([]social.SocialMemoryEntry, error)
	RecordSocialFeedbackBatch(context.Context, social.SocialFeedbackBatchInput) (social.SocialFeedbackBatchResult, error)
	UpsertSocialPersonNote(context.Context, social.SocialPersonNoteInput) (social.SocialPersonNote, error)
}

type turnMemoryPorts struct {
	promptContext    PromptContextStore
	transcriptRecall CompactedTranscriptRecallStore
	turns            TurnStore
	memoryRetrieval  MemoryRetrievalStore
	knowledge        KnowledgeRetrievalStore
	portrait         PortraitStore
	runtimeState     RuntimeStateStore
	contextRetention ContextRetentionStore
	memoryCoverage   MemoryCoverageStore
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

func memoryPortsFromStores(historyStore *history.Store, compactionStore *historycompaction.Store, runtimeStore *historyruntime.Store, memoryStore *personal.Store, extractionStore *extraction.Store, knowledgeStore *knowledge.Store, socialStore *social.Store) memoryPorts {
	if historyStore == nil || compactionStore == nil || runtimeStore == nil || memoryStore == nil || extractionStore == nil || knowledgeStore == nil || socialStore == nil {
		return memoryPorts{}
	}
	return memoryPorts{
		turn: turnMemoryPorts{
			promptContext:    historyStore,
			transcriptRecall: historyStore,
			turns:            historyStore,
			memoryRetrieval:  memoryStore,
			knowledge:        knowledgeStore,
			portrait:         memoryStore,
			runtimeState:     runtimeStore,
			contextRetention: compactionStore,
			memoryCoverage:   extractionStore,
		},
		ambient: ambientMemoryPorts{
			bindings:        historyStore,
			activity:        historyStore,
			metadata:        historyStore,
			socialRetrieval: socialStore,
			socialContext:   socialStore,
			socialLearning:  socialStore,
		},
		retention: retentionMemoryPorts{extraction: extractionStore, knowledge: knowledgeStore},
	}
}

func (p memoryPorts) ready() bool {
	return p.turn.promptContext != nil &&
		p.turn.transcriptRecall != nil &&
		p.turn.turns != nil &&
		p.turn.memoryRetrieval != nil &&
		p.turn.knowledge != nil &&
		p.turn.portrait != nil &&
		p.turn.runtimeState != nil &&
		p.turn.contextRetention != nil &&
		p.turn.memoryCoverage != nil &&
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
	_ InteractionBindingStore   = (*history.Store)(nil)
	_ ConversationActivityStore = (*history.Store)(nil)
	_ ConversationMetadataStore = (*history.Store)(nil)
	_ PromptContextStore        = (*history.Store)(nil)
	_ TurnStore                 = (*history.Store)(nil)
	_ MemoryRetrievalStore      = (*personal.Store)(nil)
	_ KnowledgeRetrievalStore   = (*knowledge.Store)(nil)
	_ PortraitStore             = (*personal.Store)(nil)
	_ SocialRetrievalStore      = (*social.Store)(nil)
	_ RuntimeStateStore         = (*historyruntime.Store)(nil)
	_ ContextRetentionStore     = (*historycompaction.Store)(nil)
	_ MemoryCoverageStore       = (*extraction.Store)(nil)
	_ extractionStore           = (*extraction.Store)(nil)
	_ knowledgeIngestStore      = (*knowledge.Store)(nil)
	_ SocialContextStore        = (*social.Store)(nil)
	_ SocialLearningStore       = (*social.Store)(nil)
	_ ModelPort                 = (*model.ModelService)(nil)
	_ CharacterLookup           = (*character.Store)(nil)
	_ ProfileSource             = (*config.ProfileStore)(nil)
	_ ConfigSource              = (*config.Reader)(nil)
	_ OwnerIdentityPort         = (*identity.Store)(nil)
)
