package coredb

import "github.com/pgvector/pgvector-go"

const currentSchemaRevision = "2026-07-30-context-retention-1"

type conversationSchema struct {
	ID          string `gorm:"type:text;primaryKey;index:conversations_character_updated,priority:3"`
	CharacterID string `gorm:"type:text;not null;index:conversations_character_updated,priority:1"`
	CreatedAtMS int64  `gorm:"not null;check:conversations_invariants_check,(created_at_ms >= 0) AND (updated_at_ms >= created_at_ms)"`
	UpdatedAtMS int64  `gorm:"not null;index:conversations_character_updated,sort:desc,priority:2"`
}

func (conversationSchema) TableName() string { return "conversations" }

type conversationTurnSchema struct {
	ID                         string  `gorm:"type:text;primaryKey"`
	ConversationID             string  `gorm:"type:text;not null;uniqueIndex:conversation_turns_conversation_sequence_key,priority:1;index:conversation_turns_conversation_status,priority:1"`
	Sequence                   int64   `gorm:"not null;check:conversation_turns_invariants_check,(sequence > 0) AND (status IN ('interpreting', 'planning', 'responding', 'completed', 'interrupted', 'failed')) AND (extraction_state IN ('ineligible', 'pending', 'claimed', 'processed', 'failed')) AND (extraction_attempt_count >= 0) AND (extraction_next_attempt_at_ms >= 0) AND (extraction_lease_expires_at_ms IS NULL OR extraction_lease_expires_at_ms >= 0) AND ((extraction_state = 'claimed') = (extraction_claim_id IS NOT NULL AND extraction_lease_owner IS NOT NULL AND extraction_lease_expires_at_ms IS NOT NULL)) AND (extraction_state = 'claimed' OR (extraction_claim_id IS NULL AND extraction_lease_owner IS NULL AND extraction_lease_expires_at_ms IS NULL)) AND (created_at_ms >= 0) AND (updated_at_ms >= created_at_ms) AND ((status = 'failed') = (error_code IS NOT NULL AND error_message IS NOT NULL));uniqueIndex:conversation_turns_conversation_sequence_key,priority:2;index:conversation_turns_conversation_status,priority:3"`
	Status                     string  `gorm:"type:text;not null;index:conversation_turns_conversation_status,priority:2"`
	Origin                     string  `gorm:"type:text;not null;default:user"`
	ErrorCode                  *string `gorm:"type:text"`
	ErrorMessage               *string `gorm:"type:text"`
	ErrorRetryable             *bool
	ExtractionState            string  `gorm:"type:text;not null;default:ineligible"`
	ExtractionClaimID          *string `gorm:"type:text;index:conversation_turns_extraction_claim"`
	ExtractionLeaseOwner       *string `gorm:"type:text"`
	ExtractionLeaseExpiresAtMS *int64
	ExtractionAttemptCount     int     `gorm:"type:integer;not null;default:0"`
	ExtractionNextAttemptAtMS  int64   `gorm:"not null;default:0"`
	ExtractionErrorCode        *string `gorm:"type:text"`
	ExtractionErrorMessage     *string `gorm:"type:text"`
	CreatedAtMS                int64   `gorm:"not null"`
	UpdatedAtMS                int64   `gorm:"not null"`
}

func (conversationTurnSchema) TableName() string { return "conversation_turns" }

type conversationTurnEvidenceSchema struct {
	TurnID      string `gorm:"type:text;primaryKey;index:conversation_turn_evidence_evidence,priority:2"`
	EvidenceID  string `gorm:"type:text;primaryKey;check:conversation_turn_evidence_invariants_check,(evidence_id <> '') AND (created_at_ms >= 0);index:conversation_turn_evidence_evidence,priority:1"`
	CreatedAtMS int64  `gorm:"not null"`
}

func (conversationTurnEvidenceSchema) TableName() string { return "conversation_turn_evidence" }

type conversationMessageSchema struct {
	ID                  string `gorm:"type:text;primaryKey"`
	ConversationID      string `gorm:"type:text;not null;uniqueIndex:conversation_messages_conversation_sequence_key,priority:1;index:conversation_messages_conversation_sequence,priority:1;index:conversation_messages_conversation_role_created,priority:1"`
	TurnID              string `gorm:"type:text;not null;uniqueIndex:conversation_messages_turn_role_key,priority:1"`
	Sequence            int64  `gorm:"not null;uniqueIndex:conversation_messages_conversation_sequence_key,priority:2;index:conversation_messages_conversation_sequence,priority:2;index:conversation_messages_conversation_role_created,sort:desc,priority:4"`
	Role                string `gorm:"type:text;not null;uniqueIndex:conversation_messages_turn_role_key,priority:2;index:conversation_messages_conversation_role_created,priority:2"`
	Content             string `gorm:"type:text;not null;check:conversation_messages_invariants_check,(sequence > 0) AND (role IN ('user', 'assistant')) AND (jsonb_typeof(expression_parts) = 'array' AND jsonb_array_length(expression_parts) <= 12) AND (content <> '' OR jsonb_array_length(expression_parts) > 0) AND (created_at_ms >= 0)"`
	ExpressionPartsJSON []byte `gorm:"column:expression_parts;type:jsonb;not null;default:'[]'::jsonb"`
	CreatedAtMS         int64  `gorm:"not null;index:conversation_messages_conversation_role_created,sort:desc,priority:3"`
}

func (conversationMessageSchema) TableName() string { return "conversation_messages" }

type stickerSchema struct {
	ID            string `gorm:"type:text;primaryKey;check:stickers_invariants_check,(id <> '') AND (content_sha256 ~ '^[0-9a-f]{64}$') AND (mime_type IN ('image/jpeg', 'image/png', 'image/gif', 'image/webp')) AND (byte_count > 0 AND byte_count <= 5242880) AND (octet_length(content) = byte_count) AND (char_length(description) <= 512 AND description = btrim(description)) AND (jsonb_typeof(tags) = 'array' AND jsonb_array_length(tags) <= 16) AND (status IN ('draft', 'active', 'disabled')) AND (status <> 'active' OR description <> '') AND (created_at_ms >= 0) AND (updated_at_ms >= created_at_ms);index:stickers_status_updated,priority:3"`
	ContentSHA256 string `gorm:"type:text;not null;unique"`
	MIMEType      string `gorm:"type:text;not null"`
	ByteCount     int64  `gorm:"not null"`
	Content       []byte `gorm:"type:bytea;not null"`
	Description   string `gorm:"type:text;not null;default:''"`
	TagsJSON      []byte `gorm:"column:tags;type:jsonb;not null;default:'[]'::jsonb"`
	Status        string `gorm:"type:text;not null;default:draft;index:stickers_status_updated,priority:1"`
	CreatedAtMS   int64  `gorm:"not null"`
	UpdatedAtMS   int64  `gorm:"not null;index:stickers_status_updated,sort:desc,priority:2"`
}

func (stickerSchema) TableName() string { return "stickers" }

type promptWindowSchema struct {
	ConversationID        string  `gorm:"type:text;primaryKey"`
	Revision              int64   `gorm:"not null;check:prompt_windows_invariants_check,(revision > 0) AND (cutoff_message_sequence >= 0) AND (updated_at_ms >= 0)"`
	Summary               *string `gorm:"type:text"`
	CutoffMessageSequence int64   `gorm:"not null;default:0"`
	ProjectionRevision    int64   `gorm:"not null;default:1"`
	ProjectionStateJSON   []byte  `gorm:"column:projection_state;type:jsonb;not null;default:'{\"version\":1,\"omissions\":[]}'::jsonb"`
	UpdatedAtMS           int64   `gorm:"not null"`
}

func (promptWindowSchema) TableName() string { return "prompt_windows" }

type turnRuntimeEventSchema struct {
	ID             string  `gorm:"type:text;primaryKey"`
	ConversationID string  `gorm:"type:text;not null;uniqueIndex:turn_runtime_events_conversation_turn_sequence_key,priority:1;index:turn_runtime_events_turn_sequence,priority:1"`
	TurnID         string  `gorm:"type:text;not null;uniqueIndex:turn_runtime_events_conversation_turn_sequence_key,priority:2;index:turn_runtime_events_turn_sequence,priority:2"`
	Sequence       int64   `gorm:"not null;check:turn_runtime_events_invariants_check,(sequence > 0) AND (created_at_ms >= 0);uniqueIndex:turn_runtime_events_conversation_turn_sequence_key,priority:3;index:turn_runtime_events_turn_sequence,priority:3;index:turn_runtime_events_type_created,priority:3"`
	EventType      string  `gorm:"type:text;not null;index:turn_runtime_events_type_created,priority:1"`
	State          *string `gorm:"type:text"`
	Code           *string `gorm:"type:text"`
	MetadataJSON   []byte  `gorm:"type:jsonb;not null;default:'{}'::jsonb"`
	CreatedAtMS    int64   `gorm:"not null;index:turn_runtime_events_type_created,priority:2"`
}

func (turnRuntimeEventSchema) TableName() string { return "turn_runtime_events" }

type toolExecutionSchema struct {
	ID                 string `gorm:"type:text;primaryKey"`
	ConversationID     string `gorm:"type:text;not null;index:tool_executions_turn_status,priority:1"`
	TurnID             string `gorm:"type:text;not null;uniqueIndex:tool_executions_turn_call_key,priority:1;uniqueIndex:tool_executions_turn_tool_key,priority:1;index:tool_executions_turn_status,priority:2"`
	CallID             string `gorm:"type:text;not null;uniqueIndex:tool_executions_turn_call_key,priority:2"`
	ToolName           string `gorm:"type:text;not null;uniqueIndex:tool_executions_turn_tool_key,priority:2"`
	Status             string `gorm:"type:text;not null;check:tool_executions_invariants_check,(status IN ('pending', 'completed', 'failed', 'cancelled')) AND (tool_name = 'desktop_observe') AND (deadline_at_ms > created_at_ms) AND (attempt_count >= 0) AND (last_dispatched_at_ms IS NULL OR last_dispatched_at_ms >= created_at_ms) AND (created_at_ms >= 0) AND (updated_at_ms >= created_at_ms) AND ((status = 'completed') = (result_media_type IS NOT NULL AND result_width IS NOT NULL AND result_height IS NOT NULL AND result_byte_count IS NOT NULL AND result_sha256 IS NOT NULL)) AND ((status IN ('failed', 'cancelled')) = (error_code IS NOT NULL AND error_message IS NOT NULL)) AND (status <> 'completed' OR (error_code IS NULL AND error_message IS NULL));index:tool_executions_turn_status,priority:3"`
	DeadlineAtMS       int64  `gorm:"not null"`
	AttemptCount       int    `gorm:"type:integer;not null;default:0"`
	LastDispatchedAtMS *int64
	ErrorCode          *string `gorm:"type:text"`
	ErrorMessage       *string `gorm:"type:text"`
	ResultMediaType    *string `gorm:"type:text"`
	ResultWidth        *int
	ResultHeight       *int
	ResultByteCount    *int
	ResultSHA256       *string `gorm:"type:text"`
	CreatedAtMS        int64   `gorm:"not null;index:tool_executions_turn_status,priority:4"`
	UpdatedAtMS        int64   `gorm:"not null"`
}

func (toolExecutionSchema) TableName() string { return "tool_executions" }

type laneContinuationSchema struct {
	ConversationID     string `gorm:"type:text;primaryKey"`
	Lane               string `gorm:"type:text;primaryKey"`
	PreviousResponseID string `gorm:"type:text;not null"`
	RequestShapeHash   string `gorm:"type:text;not null"`
	InputPrefixHash    string `gorm:"type:text;not null"`
	ResponseItemHash   string `gorm:"type:text;not null"`
	WindowRevision     int64  `gorm:"not null;check:lane_continuations_invariants_check,(window_revision > 0) AND (updated_at_ms >= 0)"`
	UpdatedAtMS        int64  `gorm:"not null"`
}

func (laneContinuationSchema) TableName() string { return "lane_continuations" }

type contextWindowSchema struct {
	ConversationID         string  `gorm:"type:text;primaryKey"`
	Lane                   string  `gorm:"type:text;primaryKey"`
	WindowNumber           int64   `gorm:"not null;check:context_windows_invariants_check,(window_number >= 0) AND (observed_prefill_tokens IS NULL OR observed_prefill_tokens >= 0) AND (estimated_prefill_tokens IS NULL OR estimated_prefill_tokens >= 0) AND (failure_count >= 0) AND (prompt_window_revision > 0) AND (updated_at_ms >= 0)"`
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
	ID                    string           `gorm:"type:text;primaryKey;index:personal_memories_scope_status,priority:5"`
	Kind                  string           `gorm:"type:text;not null"`
	ScopeKind             string           `gorm:"type:text;not null;check:personal_memories_invariants_check,(scope_kind IN ('global', 'character', 'relationship', 'unassigned_legacy')) AND (content <> '') AND (status IN ('active', 'superseded', 'tombstone')) AND (confidence_basis_points BETWEEN 0 AND 10000) AND (created_at_ms >= 0) AND (updated_at_ms >= created_at_ms) AND ((scope_kind = 'character') = (character_id IS NOT NULL) OR scope_kind <> 'character');index:personal_memories_scope_status,priority:1"`
	CharacterID           *string          `gorm:"type:text;index:personal_memories_scope_status,priority:2"`
	ReviewStatus          string           `gorm:"type:text;not null"`
	Content               string           `gorm:"type:text;not null"`
	Status                string           `gorm:"type:text;not null;index:personal_memories_scope_status,priority:3"`
	ConfidenceBasisPoints int              `gorm:"type:integer;not null"`
	SourceConversationID  string           `gorm:"type:text;not null"`
	SourceTurnID          string           `gorm:"type:text;not null"`
	EvidenceIDsJSON       []byte           `gorm:"type:jsonb;not null;default:'[]'::jsonb"`
	SupersedesID          *string          `gorm:"type:text"`
	EmbeddingModelID      *string          `gorm:"type:text"`
	EmbeddingContentHash  *string          `gorm:"type:text"`
	Embedding             *pgvector.Vector `gorm:"type:public.vector(512)"`
	CreatedAtMS           int64            `gorm:"not null"`
	UpdatedAtMS           int64            `gorm:"not null;index:personal_memories_scope_status,sort:desc,priority:4"`
}

func (personalMemorySchema) TableName() string { return "personal_memories" }

type memoryContextCoverageSchema struct {
	ConversationID string `gorm:"type:text;primaryKey;check:memory_context_coverages_invariants_check,(conversation_id <> '') AND (turn_id <> '') AND (memory_id <> '') AND (result_status IN ('applied', 'no_change')) AND (created_at_ms >= 0)"`
	TurnID         string `gorm:"type:text;primaryKey"`
	MemoryID       string `gorm:"type:text;primaryKey"`
	ResultStatus   string `gorm:"type:text;not null"`
	CreatedAtMS    int64  `gorm:"not null"`
}

func (memoryContextCoverageSchema) TableName() string { return "memory_context_coverages" }

type knowledgeEntrySchema struct {
	ID                    string           `gorm:"type:text;primaryKey;index:knowledge_entries_status_updated,priority:3"`
	Topic                 string           `gorm:"type:text;not null;check:knowledge_entries_invariants_check,(topic <> '') AND (statement <> '') AND (status IN ('candidate', 'verified', 'superseded', 'rejected', 'tombstone')) AND (confidence_basis_points BETWEEN 0 AND 10000) AND (source_url = '' OR source_url ~ '^https?://[^[:space:]]+$') AND (source_content_hash = '' OR source_content_hash ~ '^[0-9a-f]{64}$') AND (source_fetched_at_ms >= 0) AND (created_at_ms >= 0) AND (updated_at_ms >= created_at_ms)"`
	Statement             string           `gorm:"type:text;not null"`
	Status                string           `gorm:"type:text;not null;index:knowledge_entries_status_updated,priority:1"`
	VerificationBasis     string           `gorm:"type:text;not null"`
	ConfidenceBasisPoints int              `gorm:"type:integer;not null"`
	SourceConversationID  string           `gorm:"type:text;not null"`
	SourceTurnID          string           `gorm:"type:text;not null"`
	SourceURL             string           `gorm:"type:text;not null;default:'';index:knowledge_entries_source_url"`
	SourceTitle           string           `gorm:"type:text;not null;default:''"`
	SourceContentHash     string           `gorm:"type:text;not null;default:''"`
	SourceContentType     string           `gorm:"type:text;not null;default:''"`
	SourceFetchedAtMS     int64            `gorm:"not null;default:0"`
	SourceETag            string           `gorm:"column:source_etag;type:text;not null;default:''"`
	SourceLastModified    string           `gorm:"type:text;not null;default:''"`
	ReconcilerRevision    string           `gorm:"type:text;not null;default:''"`
	EvidenceText          string           `gorm:"type:text;not null;default:''"`
	SupersedesID          *string          `gorm:"type:text"`
	EmbeddingModelID      *string          `gorm:"type:text"`
	EmbeddingContentHash  *string          `gorm:"type:text"`
	Embedding             *pgvector.Vector `gorm:"type:public.vector(512)"`
	CreatedAtMS           int64            `gorm:"not null"`
	UpdatedAtMS           int64            `gorm:"not null;index:knowledge_entries_status_updated,sort:desc,priority:2"`
}

func (knowledgeEntrySchema) TableName() string { return "knowledge_entries" }

type secretValueSchema struct {
	Namespace   string `gorm:"type:text;primaryKey"`
	Name        string `gorm:"type:text;primaryKey"`
	KeyVersion  int    `gorm:"type:integer;not null;check:secret_values_invariants_check,(key_version > 0) AND (octet_length(nonce) = 12) AND (octet_length(ciphertext) > 0) AND (created_at_ms >= 0) AND (updated_at_ms >= created_at_ms)"`
	Nonce       []byte `gorm:"type:bytea;not null"`
	Ciphertext  []byte `gorm:"type:bytea;not null"`
	AAD         string `gorm:"type:text;not null"`
	CreatedAtMS int64  `gorm:"not null"`
	UpdatedAtMS int64  `gorm:"not null"`
}

func (secretValueSchema) TableName() string { return "secret_values" }

type endpointConversationSchema struct {
	CharacterID        string  `gorm:"type:text;primaryKey"`
	Endpoint           string  `gorm:"type:text;primaryKey"`
	EndpointKeyDigest  string  `gorm:"type:text;primaryKey"`
	ConversationID     string  `gorm:"type:text;not null;unique;index:endpoint_conversations_conversation"`
	Audience           string  `gorm:"type:text;not null"`
	Initiation         string  `gorm:"type:text;not null;check:endpoint_conversations_invariants_check,(endpoint IN ('desktop', 'im')) AND (endpoint_key_digest ~ '^[0-9a-f]{64}$') AND (audience IN ('single', 'multi')) AND (initiation IN ('direct', 'ambient')) AND (presentation IN ('embodied', 'chat')) AND ((principal_namespace IS NULL) = (principal_digest IS NULL)) AND ((endpoint = 'im' AND audience = 'single') = (principal_namespace IS NOT NULL AND principal_digest IS NOT NULL)) AND (principal_namespace IS NULL OR principal_namespace ~ '^[a-z0-9._-]{1,64}$') AND (principal_digest IS NULL OR principal_digest ~ '^[0-9a-f]{64}$') AND (created_at_ms >= 0) AND (updated_at_ms >= created_at_ms)"`
	Presentation       string  `gorm:"type:text;not null"`
	PrincipalNamespace *string `gorm:"type:text"`
	PrincipalDigest    *string `gorm:"type:text"`
	CreatedAtMS        int64   `gorm:"not null"`
	UpdatedAtMS        int64   `gorm:"not null"`
}

func (endpointConversationSchema) TableName() string { return "endpoint_conversations" }

type ownerIdentitySchema struct {
	Namespace     string `gorm:"type:text;primaryKey;check:owner_identities_invariants_check,(namespace ~ '^[a-z0-9._-]{1,64}$') AND (subject_digest ~ '^[0-9a-f]{64}$') AND (created_at_ms >= 0)"`
	SubjectDigest string `gorm:"type:text;primaryKey"`
	CreatedAtMS   int64  `gorm:"not null"`
}

func (ownerIdentitySchema) TableName() string { return "owner_identities" }

type socialMemoryEntrySchema struct {
	ID                 string  `gorm:"type:text;primaryKey;index:social_memory_entries_scope_kind,priority:6"`
	CharacterID        string  `gorm:"type:text;not null;index:social_memory_entries_scope_kind,priority:1;uniqueIndex:social_memory_entries_person_note_key,priority:1"`
	ConversationID     string  `gorm:"type:text;not null;uniqueIndex:social_memory_entries_scope_hash_key,priority:1;index:social_memory_entries_scope_kind,priority:2;uniqueIndex:social_memory_entries_person_note_key,priority:2"`
	Kind               string  `gorm:"type:text;not null;uniqueIndex:social_memory_entries_scope_hash_key,priority:2;index:social_memory_entries_scope_kind,priority:3"`
	Situation          string  `gorm:"type:text;not null;check:social_memory_entries_invariants_check,(kind IN ('episode', 'expression', 'behavior', 'person_note')) AND (situation <> '') AND (content <> '') AND (recall_cue <> '') AND (content_hash ~ '^[0-9a-f]{64}$') AND (status IN ('active', 'suppressed')) AND (source_start_ms > 0 AND source_end_ms >= source_start_ms) AND (use_count >= 0 AND positive_count >= 0 AND negative_count >= 0 AND unknown_count >= 0) AND ((kind = 'person_note') = (sender_id IS NOT NULL)) AND (created_at_ms >= 0) AND (updated_at_ms >= created_at_ms)"`
	Content            string  `gorm:"type:text;not null"`
	RecallCue          string  `gorm:"type:text;not null"`
	ContentHash        string  `gorm:"type:text;not null;uniqueIndex:social_memory_entries_scope_hash_key,priority:3"`
	SenderID           *string `gorm:"type:text;uniqueIndex:social_memory_entries_person_note_key,priority:3"`
	SenderName         string  `gorm:"type:text;not null;default:''"`
	LastFeedbackTurnID *string `gorm:"type:text"`
	Status             string  `gorm:"type:text;not null;index:social_memory_entries_scope_kind,priority:4"`
	SourceStartMS      int64   `gorm:"not null"`
	SourceEndMS        int64   `gorm:"not null"`
	UseCount           int64   `gorm:"not null;default:0"`
	PositiveCount      int64   `gorm:"not null;default:0"`
	NegativeCount      int64   `gorm:"not null;default:0"`
	UnknownCount       int64   `gorm:"not null;default:0"`
	CreatedAtMS        int64   `gorm:"not null"`
	UpdatedAtMS        int64   `gorm:"not null;index:social_memory_entries_scope_kind,sort:desc,priority:5"`
}

func (socialMemoryEntrySchema) TableName() string { return "social_memory_entries" }

type schemaState struct {
	ID       int16  `gorm:"primaryKey;check:fairy_schema_state_invariants_check,id = 1"`
	Revision string `gorm:"type:text;not null"`
}

func (schemaState) TableName() string { return "fairy_schema_state" }

func schemaModels() []any {
	return []any{
		&conversationSchema{},
		&conversationTurnSchema{},
		&conversationTurnEvidenceSchema{},
		&conversationMessageSchema{},
		&stickerSchema{},
		&promptWindowSchema{},
		&turnRuntimeEventSchema{},
		&toolExecutionSchema{},
		&laneContinuationSchema{},
		&contextWindowSchema{},
		&personalMemorySchema{},
		&memoryContextCoverageSchema{},
		&knowledgeEntrySchema{},
		&secretValueSchema{},
		&endpointConversationSchema{},
		&ownerIdentitySchema{},
		&socialMemoryEntrySchema{},
		&schemaState{},
	}
}
