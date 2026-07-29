//go:build integration

package coredb

import "github.com/pgvector/pgvector-go"

// These fixtures reconstruct the last multi-table schema so the destructive
// migration can be verified without keeping legacy models in production code.
type personalMemoryEvidenceSchema struct {
	MemoryID    string `gorm:"type:text;primaryKey"`
	TurnID      string `gorm:"type:text;primaryKey"`
	EvidenceID  string `gorm:"type:text;primaryKey"`
	CreatedAtMS int64  `gorm:"not null"`
}

func (personalMemoryEvidenceSchema) TableName() string { return "personal_memory_evidence" }

type knowledgeSourceSchema struct {
	KnowledgeID  string `gorm:"type:text;primaryKey"`
	SourceID     string `gorm:"type:text;primaryKey"`
	Title        string `gorm:"type:text;not null"`
	URL          string `gorm:"type:text;not null"`
	CanonicalURL string `gorm:"type:text;not null;default:''"`
	Snippet      string `gorm:"type:text;not null"`
	Rank         int    `gorm:"type:integer;not null"`
	FetchedAtMS  int64  `gorm:"not null"`
}

func (knowledgeSourceSchema) TableName() string { return "knowledge_sources" }

type knowledgeDocumentVersionSchema struct {
	ID                 string `gorm:"type:text;primaryKey"`
	DocumentID         string `gorm:"type:text;not null"`
	ContentHash        string `gorm:"type:text;not null"`
	ContentType        string `gorm:"type:text;not null"`
	Status             string `gorm:"type:text;not null"`
	FetchedAtMS        int64  `gorm:"not null"`
	ETag               string `gorm:"column:etag;type:text;not null;default:''"`
	LastModified       string `gorm:"type:text;not null;default:''"`
	ReconcilerRevision string `gorm:"type:text;not null;default:''"`
	CreatedAtMS        int64  `gorm:"not null"`
}

func (knowledgeDocumentVersionSchema) TableName() string { return "knowledge_document_versions" }

type knowledgeChunkSchema struct {
	ID                   string           `gorm:"type:text;primaryKey"`
	VersionID            string           `gorm:"type:text;not null"`
	Ordinal              int              `gorm:"not null"`
	Text                 string           `gorm:"type:text;not null"`
	TextHash             string           `gorm:"type:text;not null"`
	EmbeddingModelID     *string          `gorm:"type:text"`
	EmbeddingContentHash *string          `gorm:"type:text"`
	Embedding            *pgvector.Vector `gorm:"type:public.vector(512)"`
	CreatedAtMS          int64            `gorm:"not null"`
}

func (knowledgeChunkSchema) TableName() string { return "knowledge_chunks" }

type knowledgeEvidenceSchema struct {
	KnowledgeID     string `gorm:"type:text;primaryKey"`
	ChunkID         string `gorm:"type:text;primaryKey"`
	VersionID       string `gorm:"type:text;not null"`
	Active          bool   `gorm:"not null;default:true"`
	CreatedAtMS     int64  `gorm:"not null"`
	InvalidatedAtMS *int64
}

func (knowledgeEvidenceSchema) TableName() string { return "knowledge_evidence" }

type extractionBatchSchema struct {
	ID                string  `gorm:"type:text;primaryKey"`
	ConversationID    string  `gorm:"type:text;not null"`
	CharacterID       string  `gorm:"type:text;not null"`
	Status            string  `gorm:"type:text;not null"`
	FirstTurnSequence int64   `gorm:"not null"`
	LastTurnSequence  int64   `gorm:"not null"`
	LeaseOwner        *string `gorm:"type:text"`
	LeaseExpiresAtMS  *int64
	AttemptCount      int     `gorm:"not null;default:0"`
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
	TaskID           string  `gorm:"type:text;not null;default:''"`
	SourceJSON       []byte  `gorm:"type:jsonb;not null"`
	Status           string  `gorm:"type:text;not null"`
	LeaseOwner       *string `gorm:"type:text"`
	LeaseExpiresAtMS *int64
	AttemptCount     int     `gorm:"not null;default:0"`
	NextAttemptAtMS  int64   `gorm:"not null;default:0"`
	ErrorCategory    *string `gorm:"type:text"`
	ErrorMessage     *string `gorm:"type:text"`
	CreatedAtMS      int64   `gorm:"not null"`
	UpdatedAtMS      int64   `gorm:"not null"`
}

func (knowledgeIngestJobSchema) TableName() string { return "knowledge_ingest_jobs" }

type socialReplyFeedbackSchema struct {
	ID                   string `gorm:"type:text;primaryKey"`
	CharacterID          string `gorm:"type:text;not null"`
	ConversationID       string `gorm:"type:text;not null"`
	TurnID               string `gorm:"type:text;not null"`
	Outcome              string `gorm:"type:text;not null"`
	EntryIDsJSON         []byte `gorm:"type:jsonb;not null"`
	ObservedMessageCount int    `gorm:"type:integer;not null"`
	CreatedAtMS          int64  `gorm:"not null"`
}

func (socialReplyFeedbackSchema) TableName() string { return "social_reply_feedback" }

type socialPersonNoteSchema struct {
	ID             string `gorm:"type:text;primaryKey"`
	CharacterID    string `gorm:"type:text;not null"`
	ConversationID string `gorm:"type:text;not null"`
	SenderID       string `gorm:"type:text;not null"`
	SenderName     string `gorm:"type:text;not null;default:''"`
	Note           string `gorm:"type:text;not null"`
	CreatedAtMS    int64  `gorm:"not null"`
	UpdatedAtMS    int64  `gorm:"not null"`
}

func (socialPersonNoteSchema) TableName() string { return "social_person_notes" }
