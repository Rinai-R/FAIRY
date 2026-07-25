package postgres

// Repository family ownership inside the memory PostgreSQL adapter.
const (
	FamilyConversation = "conversation" // conversation.go, store_conversation.go
	FamilyPersonal     = "personal"     // personal.go, personal_retrieval.go, store_personal.go
	FamilySocial       = "social"       // social.go, social_person.go, store_social.go
	FamilyExtraction   = "extraction"   // extraction.go, store_extraction_*.go
	FamilyRetrieval    = "retrieval"    // retrieval.go, personal_retrieval.go, store_retrieval*.go
	FamilyCompaction   = "compaction"   // compaction.go, store_compaction.go
	FamilyKnowledge    = "knowledge"    // knowledge.go, knowledge_ingest.go, store_knowledge*.go
	FamilyRuntime      = "runtime"      // runtime_state.go, store_runtime_state.go
	FamilyEmbedding    = "embedding"    // embedding.go, store_embedding*.go
	FamilyMetrics      = "metrics"      // metrics.go, store_metrics.go
	FamilyStore        = "store"        // store.go and store_* orchestration
)
