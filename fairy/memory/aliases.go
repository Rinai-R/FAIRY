package memory

import (
	mempostgres "fairy/internal/adapters/memory/postgres"
	domainmemory "fairy/internal/domain/memory"
)

// Compaction

type CompactionResult = domainmemory.CompactionResult

// Conversation

type ConversationRecord = domainmemory.ConversationRecord
type MessageRecord = domainmemory.MessageRecord
type PromptWindowRecord = domainmemory.PromptWindowRecord
type ConversationBootstrap = domainmemory.ConversationBootstrap
type PersistedTurn = domainmemory.PersistedTurn

// Personal memory

const MaxPersonalMemoryContentRunes = domainmemory.MaxPersonalMemoryContentRunes

type MemoryScope = domainmemory.MemoryScope
type PersonalMemoryRecord = domainmemory.PersonalMemoryRecord
type PersonalMemoryCatalog = domainmemory.PersonalMemoryCatalog

// Extraction

const (
	DefaultExtractionBatchLimit = domainmemory.DefaultExtractionBatchLimit
	MaxMemoryMutationsPerBatch  = domainmemory.MaxMemoryMutationsPerBatch
)

type ExtractionTurn = domainmemory.ExtractionTurn
type ExtractionBatchInput = domainmemory.ExtractionBatchInput
type MemoryMutation = domainmemory.MemoryMutation
type MemoryMutationResult = domainmemory.MemoryMutationResult
type MemoryMutationOutput = domainmemory.MemoryMutationOutput

// Knowledge

type KnowledgeRecord = domainmemory.KnowledgeRecord
type KnowledgeCatalog = domainmemory.KnowledgeCatalog
type ExtractionBatchRecord = domainmemory.ExtractionBatchRecord
type ExtractionBatchCatalog = domainmemory.ExtractionBatchCatalog
type AssistantSource = domainmemory.AssistantSource
type KnowledgeIngestSnapshot = domainmemory.KnowledgeIngestSnapshot

// Message paging

const (
	MaxMessagePageLimit     = 200
	DefaultMessagePageLimit = 50
)

type MessagePage = domainmemory.MessagePage

// Retrieval

type RetrievedPersonalMemory = domainmemory.RetrievedPersonalMemory
type RetrievedKnowledge = domainmemory.RetrievedKnowledge
type RetrievalContext = domainmemory.RetrievalContext

// Runtime state

const (
	PromptLaneRespond   = "respond"
	PromptLaneCompact   = "compact"
	PromptLaneExtract   = "extract"
	PromptLaneTranslate = "translate"
)

type TurnRuntimeEventInput = domainmemory.TurnRuntimeEventInput
type TurnRuntimeEventRecord = domainmemory.TurnRuntimeEventRecord
type LaneContinuationRecord = domainmemory.LaneContinuationRecord
type ContextWindowRecord = domainmemory.ContextWindowRecord

// Social memory

const (
	SocialMemoryEpisode    = domainmemory.SocialMemoryEpisode
	SocialMemoryExpression = domainmemory.SocialMemoryExpression
	SocialMemoryBehavior   = domainmemory.SocialMemoryBehavior

	SocialFeedbackPositive = domainmemory.SocialFeedbackPositive
	SocialFeedbackNegative = domainmemory.SocialFeedbackNegative
	SocialFeedbackUnknown  = domainmemory.SocialFeedbackUnknown

	MaxSocialSituationRunes = domainmemory.MaxSocialSituationRunes
	MaxSocialContentRunes   = domainmemory.MaxSocialContentRunes
	MaxSocialRecallRunes    = domainmemory.MaxSocialRecallRunes
)

type SocialMemoryEntryInput = domainmemory.SocialMemoryEntryInput
type SocialMemoryBatchInput = domainmemory.SocialMemoryBatchInput
type SocialMemoryEntry = domainmemory.SocialMemoryEntry
type SocialMemoryContext = domainmemory.SocialMemoryContext
type SocialReplyFeedbackInput = domainmemory.SocialReplyFeedbackInput
type SocialReplyFeedback = domainmemory.SocialReplyFeedback
type RecentSocialFeedbackSummary = domainmemory.RecentSocialFeedbackSummary

// Social person notes

const MaxSocialPersonNoteRunes = 240

type SocialPersonNoteInput = domainmemory.SocialPersonNoteInput
type SocialPersonNote = domainmemory.SocialPersonNote

// Store metrics and embedding

type (
	EmbeddingJobResult         = mempostgres.EmbeddingJobResult
	VectorIndex                = mempostgres.VectorIndex
	SemanticVectorIndex        = mempostgres.SemanticVectorIndex
	VectorMaintenanceIndex     = mempostgres.VectorMaintenanceIndex
	VectorRebuildResult        = mempostgres.VectorRebuildResult
	VectorReconciliationResult = mempostgres.VectorReconciliationResult
	UsageReport                = mempostgres.UsageReport
	UsageLaneAggregate         = mempostgres.UsageLaneAggregate
	UsageTurn                  = mempostgres.UsageTurn
	VectorMetrics              = mempostgres.VectorMetrics
)

const SocialNegativeSuppressThreshold = mempostgres.SocialNegativeSuppressThreshold

// Semantic embedding readiness

const SemanticDatabaseStatusReady = "ready"

// SemanticEmbeddingReadiness reports PostgreSQL embedding queue state.
type SemanticEmbeddingReadiness struct {
	Dimensions     int    `json:"dimensions"`
	DatabaseStatus string `json:"databaseStatus"`
	SemanticStatus string `json:"semanticStatus"`
	Reason         string `json:"reason"`
	PendingJobs    int64  `json:"pendingJobs"`
	RunningJobs    int64  `json:"runningJobs"`
	FailedJobs     int64  `json:"failedJobs"`
	EmbeddedItems  int64  `json:"embeddedItems"`
	VectorRows     int64  `json:"vectorRows"`
}
