package postgres

import domainmemory "fairy/internal/domain/memory"

type (
	Summary                     = domainmemory.Summary
	ConversationRecord          = domainmemory.ConversationRecord
	MessageRecord               = domainmemory.MessageRecord
	PromptWindowRecord          = domainmemory.PromptWindowRecord
	ConversationBootstrap       = domainmemory.ConversationBootstrap
	PersistedTurn               = domainmemory.PersistedTurn
	MemoryScope                 = domainmemory.MemoryScope
	PersonalMemoryRecord        = domainmemory.PersonalMemoryRecord
	PersonalMemoryCatalog       = domainmemory.PersonalMemoryCatalog
	SocialMemoryBatchInput      = domainmemory.SocialMemoryBatchInput
	SocialMemoryEntry           = domainmemory.SocialMemoryEntry
	SocialMemoryEntryInput      = domainmemory.SocialMemoryEntryInput
	SocialMemoryContext         = domainmemory.SocialMemoryContext
	SocialReplyFeedbackInput    = domainmemory.SocialReplyFeedbackInput
	SocialReplyFeedback         = domainmemory.SocialReplyFeedback
	RecentSocialFeedbackSummary = domainmemory.RecentSocialFeedbackSummary
	SocialPersonNoteInput       = domainmemory.SocialPersonNoteInput
	SocialPersonNote            = domainmemory.SocialPersonNote
	ExtractionTurn              = domainmemory.ExtractionTurn
	ExtractionBatchInput        = domainmemory.ExtractionBatchInput
	MemoryMutation              = domainmemory.MemoryMutation
	MemoryMutationResult        = domainmemory.MemoryMutationResult
	MemoryMutationOutput        = domainmemory.MemoryMutationOutput
	KnowledgeRecord             = domainmemory.KnowledgeRecord
	KnowledgeCatalog            = domainmemory.KnowledgeCatalog
	ExtractionBatchRecord       = domainmemory.ExtractionBatchRecord
	ExtractionBatchCatalog      = domainmemory.ExtractionBatchCatalog
	AssistantSource             = domainmemory.AssistantSource
	KnowledgeIngestSnapshot     = domainmemory.KnowledgeIngestSnapshot
	RetrievedPersonalMemory     = domainmemory.RetrievedPersonalMemory
	RetrievedKnowledge          = domainmemory.RetrievedKnowledge
	RetrievalContext            = domainmemory.RetrievalContext
	CompactionResult            = domainmemory.CompactionResult
	TurnRuntimeEventInput       = domainmemory.TurnRuntimeEventInput
	TurnRuntimeEventRecord      = domainmemory.TurnRuntimeEventRecord
	LaneContinuationRecord      = domainmemory.LaneContinuationRecord
	ContextWindowRecord         = domainmemory.ContextWindowRecord
	MessagePage                 = domainmemory.MessagePage
	VectorMetrics               = domainmemory.VectorMetrics
)
