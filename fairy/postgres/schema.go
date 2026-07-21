package postgres

type conversationSchema struct {
	ID          string `gorm:"type:text;primaryKey"`
	CharacterID string `gorm:"type:text;not null"`
	CreatedAtMS int64  `gorm:"not null"`
	UpdatedAtMS int64  `gorm:"not null"`
}

func (conversationSchema) TableName() string { return "conversations" }

type conversationTurnSchema struct {
	ID              string  `gorm:"type:text;primaryKey"`
	ConversationID  string  `gorm:"type:text;not null"`
	Sequence        int64   `gorm:"not null"`
	Status          string  `gorm:"type:text;not null"`
	ErrorCode       *string `gorm:"type:text"`
	ErrorMessage    *string `gorm:"type:text"`
	ErrorRetryable  *bool
	ExtractionState string `gorm:"type:text;not null;default:ineligible"`
	CreatedAtMS     int64  `gorm:"not null"`
	UpdatedAtMS     int64  `gorm:"not null"`
}

func (conversationTurnSchema) TableName() string { return "conversation_turns" }

type conversationMessageSchema struct {
	ID             string `gorm:"type:text;primaryKey"`
	ConversationID string `gorm:"type:text;not null"`
	TurnID         string `gorm:"type:text;not null"`
	Sequence       int64  `gorm:"not null"`
	Role           string `gorm:"type:text;not null"`
	Content        string `gorm:"type:text;not null"`
	CreatedAtMS    int64  `gorm:"not null"`
}

func (conversationMessageSchema) TableName() string { return "conversation_messages" }

type promptWindowSchema struct {
	ConversationID        string  `gorm:"type:text;primaryKey"`
	Revision              int64   `gorm:"not null"`
	Summary               *string `gorm:"type:text"`
	CutoffMessageSequence int64   `gorm:"not null;default:0"`
	UpdatedAtMS           int64   `gorm:"not null"`
}

func (promptWindowSchema) TableName() string { return "prompt_windows" }

type turnRuntimeEventSchema struct {
	ID             string  `gorm:"type:text;primaryKey"`
	ConversationID string  `gorm:"type:text;not null"`
	TurnID         string  `gorm:"type:text;not null"`
	Sequence       int64   `gorm:"not null"`
	EventType      string  `gorm:"type:text;not null"`
	State          *string `gorm:"type:text"`
	Code           *string `gorm:"type:text"`
	MetadataJSON   []byte  `gorm:"type:jsonb;not null;default:'{}'::jsonb"`
	CreatedAtMS    int64   `gorm:"not null"`
}

func (turnRuntimeEventSchema) TableName() string { return "turn_runtime_events" }

type laneContinuationSchema struct {
	ConversationID     string `gorm:"type:text;primaryKey"`
	Lane               string `gorm:"type:text;primaryKey"`
	PreviousResponseID string `gorm:"type:text;not null"`
	RequestShapeHash   string `gorm:"type:text;not null"`
	InputPrefixHash    string `gorm:"type:text;not null"`
	ResponseItemHash   string `gorm:"type:text;not null"`
	WindowRevision     int64  `gorm:"not null"`
	UpdatedAtMS        int64  `gorm:"not null"`
}

func (laneContinuationSchema) TableName() string { return "lane_continuations" }

type contextWindowSchema struct {
	ConversationID         string  `gorm:"type:text;primaryKey"`
	Lane                   string  `gorm:"type:text;primaryKey"`
	WindowNumber           int64   `gorm:"not null"`
	FirstWindowID          string  `gorm:"type:text;not null"`
	PreviousWindowID       *string `gorm:"type:text"`
	WindowID               string  `gorm:"type:text;not null"`
	ObservedPrefillTokens  *int64
	EstimatedPrefillTokens *int64
	LastTrigger            string `gorm:"type:text;not null;default:created"`
	FailureCount           int64  `gorm:"not null;default:0"`
	PromptWindowRevision   int64  `gorm:"not null"`
	UpdatedAtMS            int64  `gorm:"not null"`
}

func (contextWindowSchema) TableName() string { return "context_windows" }

type personalMemorySchema struct {
	ID                    string  `gorm:"type:text;primaryKey"`
	Kind                  string  `gorm:"type:text;not null"`
	ScopeKind             string  `gorm:"type:text;not null"`
	CharacterID           *string `gorm:"type:text"`
	ReviewStatus          string  `gorm:"type:text;not null"`
	Content               string  `gorm:"type:text;not null"`
	Status                string  `gorm:"type:text;not null"`
	ConfidenceBasisPoints int     `gorm:"type:integer;not null"`
	SourceConversationID  string  `gorm:"type:text;not null"`
	SourceTurnID          string  `gorm:"type:text;not null"`
	SupersedesID          *string `gorm:"type:text"`
	CreatedAtMS           int64   `gorm:"not null"`
	UpdatedAtMS           int64   `gorm:"not null"`
}

func (personalMemorySchema) TableName() string { return "personal_memories" }

type knowledgeEntrySchema struct {
	ID                    string  `gorm:"type:text;primaryKey"`
	Topic                 string  `gorm:"type:text;not null"`
	Statement             string  `gorm:"type:text;not null"`
	Status                string  `gorm:"type:text;not null"`
	VerificationBasis     string  `gorm:"type:text;not null"`
	ConfidenceBasisPoints int     `gorm:"type:integer;not null"`
	SourceConversationID  string  `gorm:"type:text;not null"`
	SourceTurnID          string  `gorm:"type:text;not null"`
	SupersedesID          *string `gorm:"type:text"`
	CreatedAtMS           int64   `gorm:"not null"`
	UpdatedAtMS           int64   `gorm:"not null"`
}

func (knowledgeEntrySchema) TableName() string { return "knowledge_entries" }

type knowledgeSourceSchema struct {
	KnowledgeID string `gorm:"type:text;primaryKey"`
	SourceID    string `gorm:"type:text;primaryKey"`
	Title       string `gorm:"type:text;not null"`
	URL         string `gorm:"type:text;not null"`
	Snippet     string `gorm:"type:text;not null"`
	Rank        int    `gorm:"type:integer;not null"`
	FetchedAtMS int64  `gorm:"not null"`
}

func (knowledgeSourceSchema) TableName() string { return "knowledge_sources" }

type extractionBatchSchema struct {
	ID                string  `gorm:"type:text;primaryKey"`
	ConversationID    string  `gorm:"type:text;not null"`
	CharacterID       string  `gorm:"type:text;not null"`
	Status            string  `gorm:"type:text;not null"`
	FirstTurnSequence int64   `gorm:"not null"`
	LastTurnSequence  int64   `gorm:"not null"`
	LeaseOwner        *string `gorm:"type:text"`
	LeaseExpiresAtMS  *int64
	AttemptCount      int     `gorm:"type:integer;not null;default:0"`
	ErrorCode         *string `gorm:"type:text"`
	ErrorMessage      *string `gorm:"type:text"`
	ErrorRetryable    *bool
	CreatedAtMS       int64 `gorm:"not null"`
	UpdatedAtMS       int64 `gorm:"not null"`
}

func (extractionBatchSchema) TableName() string { return "extraction_batches" }

type extractionBatchTurnSchema struct {
	BatchID      string `gorm:"type:text;primaryKey"`
	TurnID       string `gorm:"type:text;primaryKey"`
	TurnSequence int64  `gorm:"not null"`
}

func (extractionBatchTurnSchema) TableName() string { return "extraction_batch_turns" }

type knowledgeIngestJobSchema struct {
	ID               string  `gorm:"type:text;primaryKey"`
	ConversationID   string  `gorm:"type:text;not null"`
	TurnID           string  `gorm:"type:text;not null"`
	Query            string  `gorm:"type:text;not null;default:''"`
	Title            string  `gorm:"type:text;not null;default:''"`
	URL              string  `gorm:"type:text;not null;default:''"`
	Snippet          string  `gorm:"type:text;not null;default:''"`
	Rank             int     `gorm:"type:integer;not null;default:0"`
	FetchedAtMS      int64   `gorm:"not null;default:0"`
	Status           string  `gorm:"type:text;not null"`
	LeaseOwner       *string `gorm:"type:text"`
	LeaseExpiresAtMS *int64
	AttemptCount     int     `gorm:"type:integer;not null;default:0"`
	ErrorMessage     *string `gorm:"type:text"`
	CreatedAtMS      int64   `gorm:"not null"`
	UpdatedAtMS      int64   `gorm:"not null"`
}

func (knowledgeIngestJobSchema) TableName() string { return "knowledge_ingest_jobs" }

type memoryEmbeddingItemSchema struct {
	ID                string  `gorm:"type:text;primaryKey"`
	ItemKind          string  `gorm:"type:text;not null"`
	ItemID            string  `gorm:"type:text;not null"`
	ModelID           string  `gorm:"type:text;not null"`
	Dimensions        int     `gorm:"type:integer;not null"`
	PointID           string  `gorm:"type:uuid;not null"`
	ContentHash       string  `gorm:"type:text;not null"`
	Status            string  `gorm:"type:text;not null"`
	ErrorCode         *string `gorm:"type:text"`
	ErrorMessage      *string `gorm:"type:text"`
	EmbeddedAtMS      *int64
	LegacyVectorRowID *int64 `gorm:"column:legacy_vector_rowid"`
	CreatedAtMS       int64  `gorm:"not null"`
	UpdatedAtMS       int64  `gorm:"not null"`
}

func (memoryEmbeddingItemSchema) TableName() string { return "memory_embedding_items" }

type memoryEmbeddingJobSchema struct {
	ID               string  `gorm:"type:text;primaryKey"`
	ItemKind         string  `gorm:"type:text;not null"`
	ItemID           string  `gorm:"type:text;not null"`
	ModelID          string  `gorm:"type:text;not null"`
	Dimensions       int     `gorm:"type:integer;not null"`
	PointID          string  `gorm:"type:uuid;not null"`
	ContentHash      string  `gorm:"type:text;not null"`
	Status           string  `gorm:"type:text;not null"`
	LeaseOwner       *string `gorm:"type:text"`
	LeaseExpiresAtMS *int64
	AttemptCount     int     `gorm:"type:integer;not null;default:0"`
	ErrorCode        *string `gorm:"type:text"`
	ErrorMessage     *string `gorm:"type:text"`
	Retryable        bool    `gorm:"not null;default:false"`
	CreatedAtMS      int64   `gorm:"not null"`
	UpdatedAtMS      int64   `gorm:"not null"`
}

func (memoryEmbeddingJobSchema) TableName() string { return "memory_embedding_jobs" }

type secretValueSchema struct {
	Namespace   string `gorm:"type:text;primaryKey"`
	Name        string `gorm:"type:text;primaryKey"`
	KeyVersion  int    `gorm:"type:integer;not null"`
	Nonce       []byte `gorm:"type:bytea;not null"`
	Ciphertext  []byte `gorm:"type:bytea;not null"`
	AAD         string `gorm:"type:text;not null"`
	CreatedAtMS int64  `gorm:"not null"`
	UpdatedAtMS int64  `gorm:"not null"`
}

func (secretValueSchema) TableName() string { return "secret_values" }

type vectorRebuildRunSchema struct {
	ID             string  `gorm:"type:text;primaryKey"`
	CollectionName string  `gorm:"type:text;not null"`
	ModelID        string  `gorm:"type:text;not null"`
	Status         string  `gorm:"type:text;not null"`
	ScannedItems   int64   `gorm:"not null;default:0"`
	UpsertedPoints int64   `gorm:"not null;default:0"`
	CheckpointJSON []byte  `gorm:"type:jsonb;not null;default:'{}'::jsonb"`
	ErrorMessage   *string `gorm:"type:text"`
	CreatedAtMS    int64   `gorm:"not null"`
	UpdatedAtMS    int64   `gorm:"not null"`
}

func (vectorRebuildRunSchema) TableName() string { return "vector_rebuild_runs" }

type vectorReconciliationRunSchema struct {
	ID             string  `gorm:"type:text;primaryKey"`
	CollectionName string  `gorm:"type:text;not null"`
	DryRun         bool    `gorm:"not null"`
	Status         string  `gorm:"type:text;not null"`
	MissingPoints  int64   `gorm:"not null;default:0"`
	StalePoints    int64   `gorm:"not null;default:0"`
	OrphanPoints   int64   `gorm:"not null;default:0"`
	ReportJSON     []byte  `gorm:"type:jsonb;not null;default:'{}'::jsonb"`
	ErrorMessage   *string `gorm:"type:text"`
	CreatedAtMS    int64   `gorm:"not null"`
	UpdatedAtMS    int64   `gorm:"not null"`
}

func (vectorReconciliationRunSchema) TableName() string { return "vector_reconciliation_runs" }

type surfaceConversationSchema struct {
	CharacterID      string `gorm:"type:text;primaryKey"`
	Surface          string `gorm:"type:text;primaryKey"`
	SurfaceKeyDigest string `gorm:"type:text;primaryKey"`
	ConversationID   string `gorm:"type:text;not null"`
	CreatedAtMS      int64  `gorm:"not null"`
	UpdatedAtMS      int64  `gorm:"not null"`
}

func (surfaceConversationSchema) TableName() string { return "surface_conversations" }

func schemaModels() []any {
	return []any{
		&conversationSchema{},
		&conversationTurnSchema{},
		&conversationMessageSchema{},
		&promptWindowSchema{},
		&turnRuntimeEventSchema{},
		&laneContinuationSchema{},
		&contextWindowSchema{},
		&personalMemorySchema{},
		&knowledgeEntrySchema{},
		&knowledgeSourceSchema{},
		&extractionBatchSchema{},
		&extractionBatchTurnSchema{},
		&knowledgeIngestJobSchema{},
		&memoryEmbeddingItemSchema{},
		&memoryEmbeddingJobSchema{},
		&secretValueSchema{},
		&vectorRebuildRunSchema{},
		&vectorReconciliationRunSchema{},
		&surfaceConversationSchema{},
	}
}

func schemaTableNames() []string {
	return []string{
		"conversations",
		"conversation_turns",
		"conversation_messages",
		"prompt_windows",
		"turn_runtime_events",
		"lane_continuations",
		"context_windows",
		"personal_memories",
		"knowledge_entries",
		"knowledge_sources",
		"extraction_batches",
		"extraction_batch_turns",
		"knowledge_ingest_jobs",
		"memory_embedding_items",
		"memory_embedding_jobs",
		"secret_values",
		"vector_rebuild_runs",
		"vector_reconciliation_runs",
		"surface_conversations",
	}
}
