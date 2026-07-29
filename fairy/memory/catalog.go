package memory

type AssistantSource struct {
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type KnowledgeRecord struct {
	ID                    string            `json:"id"`
	Topic                 string            `json:"topic"`
	Statement             string            `json:"statement"`
	Status                string            `json:"status"`
	VerificationBasis     string            `json:"verificationBasis"`
	ConfidenceBasisPoints uint16            `json:"confidenceBasisPoints"`
	SourceConversationID  string            `json:"sourceConversationId"`
	SourceTurnID          string            `json:"sourceTurnId"`
	SupersedesID          *string           `json:"supersedesId"`
	Subject               *string           `json:"subject,omitempty"`
	Predicate             *string           `json:"predicate,omitempty"`
	Value                 *string           `json:"value,omitempty"`
	Sources               []AssistantSource `json:"sources"`
	CreatedAtUnixMS       int64             `json:"createdAtUnixMs"`
	UpdatedAtUnixMS       int64             `json:"updatedAtUnixMs"`
}

type KnowledgeCatalog struct {
	Candidates []KnowledgeRecord `json:"candidates"`
	Verified   []KnowledgeRecord `json:"verified"`
}

type KnowledgeIngestJobRecord struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversationId"`
	TurnID          string `json:"turnId"`
	BatchID         string `json:"batchId"`
	Status          string `json:"status"`
	AttemptCount    int    `json:"attemptCount"`
	NextAttemptAtMS int64  `json:"nextAttemptAtMs"`
	ErrorCategory   string `json:"errorCategory,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	CreatedAtMS     int64  `json:"createdAtMs"`
	UpdatedAtMS     int64  `json:"updatedAtMs"`
}

type WireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type ExtractionBatchRecord struct {
	ID                string     `json:"id"`
	ConversationID    string     `json:"conversationId"`
	CharacterID       string     `json:"characterId"`
	Status            string     `json:"status"`
	FirstTurnSequence uint64     `json:"firstTurnSequence"`
	LastTurnSequence  uint64     `json:"lastTurnSequence"`
	Error             *WireError `json:"error"`
	CreatedAtUnixMS   int64      `json:"createdAtUnixMs"`
	UpdatedAtUnixMS   int64      `json:"updatedAtUnixMs"`
}

type ExtractionBatchCatalog struct {
	Running []ExtractionBatchRecord `json:"running"`
	Failed  []ExtractionBatchRecord `json:"failed"`
}

type RetrievedKnowledge struct {
	ID                    string            `json:"id"`
	Layer                 string            `json:"layer"`
	Topic                 string            `json:"topic"`
	Statement             string            `json:"statement"`
	VerificationBasis     string            `json:"verificationBasis"`
	ConfidenceBasisPoints uint16            `json:"confidenceBasisPoints"`
	Sources               []AssistantSource `json:"sources"`
	UpdatedAtUnixMS       int64             `json:"updatedAtUnixMs"`
	TextScore             float64           `json:"-"`
}

type RetrievalContext struct {
	PersonalMemories []RetrievedPersonalMemory `json:"personalMemories"`
	Knowledge        []RetrievedKnowledge      `json:"knowledge"`
	SocialMemories   SocialMemoryContext       `json:"socialMemories,omitempty"`
	// SemanticStatus is non-secret metadata for callers; empty means legacy FTS-only.
	SemanticStatus string `json:"semanticStatus,omitempty"`
}

func (c RetrievalContext) Empty() bool {
	return len(c.PersonalMemories) == 0 && len(c.Knowledge) == 0 && c.SocialMemories.Empty()
}

type SocialPersonNoteInput struct {
	CharacterID    string
	ConversationID string
	SenderID       string
	SenderName     string
	Note           string
}

type SocialPersonNote struct {
	ID              string
	CharacterID     string
	ConversationID  string
	SenderID        string
	SenderName      string
	Note            string
	UpdatedAtUnixMS int64
}

type TurnRuntimeEventInput struct {
	ConversationID string  `json:"conversationId"`
	TurnID         string  `json:"turnId"`
	EventType      string  `json:"eventType"`
	State          *string `json:"state,omitempty"`
	Code           *string `json:"code,omitempty"`
	MetadataJSON   string  `json:"metadataJson"`
}

type TurnRuntimeEventRecord struct {
	ID              string  `json:"id"`
	ConversationID  string  `json:"conversationId"`
	TurnID          string  `json:"turnId"`
	Sequence        uint64  `json:"sequence"`
	EventType       string  `json:"eventType"`
	State           *string `json:"state,omitempty"`
	Code            *string `json:"code,omitempty"`
	MetadataJSON    string  `json:"metadataJson"`
	CreatedAtUnixMS int64   `json:"createdAtUnixMs"`
}

type LaneContinuationRecord struct {
	ConversationID     string `json:"conversationId"`
	Lane               string `json:"lane"`
	PreviousResponseID string `json:"previousResponseId"`
	RequestShapeHash   string `json:"requestShapeHash"`
	InputPrefixHash    string `json:"inputPrefixHash"`
	ResponseItemHash   string `json:"responseItemHash"`
	WindowRevision     uint64 `json:"windowRevision"`
	UpdatedAtUnixMS    int64  `json:"updatedAtUnixMs"`
}

type Summary struct {
	Conversations           int64 `json:"conversations"`
	ActiveGlobalMemories    int64 `json:"activeGlobalMemories"`
	ActiveCharacterMemories int64 `json:"activeCharacterMemories"`
	NeedsReviewMemories     int64 `json:"needsReviewMemories"`
	PendingExtractionTurns  int64 `json:"pendingExtractionTurns"`
	RunningBatches          int64 `json:"runningBatches"`
	FailedBatches           int64 `json:"failedBatches"`
	CandidateKnowledge      int64 `json:"candidateKnowledge"`
	VerifiedKnowledge       int64 `json:"verifiedKnowledge"`
	ReadOnly                bool  `json:"readOnly"`
}

type MessagePage struct {
	Messages           []MessageRecord `json:"messages"`
	NextBeforeSequence *uint64         `json:"nextBeforeSequence,omitempty"`
}

type KnowledgeIngestSnapshot struct {
	ConversationID  string
	TurnID          string
	Query           string
	Title           string
	URL             string
	Snippet         string
	Rank            uint8
	FetchedAtUnixMS int64
}

const (
	MaxKnowledgeIngestSources         = 5
	MaxKnowledgeIngestSourceJSONBytes = 16 << 10
	MaxKnowledgeIngestTitleRunes      = 300
	MaxKnowledgeIngestSnippetRunes    = 1200
)

type KnowledgeIngestSource struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type KnowledgeIngestBatch struct {
	ID             string                  `json:"id"`
	ConversationID string                  `json:"conversationId"`
	TurnID         string                  `json:"turnId"`
	Sources        []KnowledgeIngestSource `json:"sources"`
}

type KnowledgeIngestClaim struct {
	JobID string               `json:"jobId"`
	Batch KnowledgeIngestBatch `json:"batch"`
}

type KnowledgeDocumentChunk struct {
	ID       string `json:"id"`
	Ordinal  int    `json:"ordinal"`
	Text     string `json:"text"`
	TextHash string `json:"textHash"`
}

type KnowledgeDocument struct {
	SourceID        string                   `json:"sourceId"`
	CanonicalURL    string                   `json:"canonicalUrl"`
	Title           string                   `json:"title"`
	ContentHash     string                   `json:"contentHash"`
	ContentType     string                   `json:"contentType"`
	ETag            string                   `json:"etag"`
	LastModified    string                   `json:"lastModified"`
	FetchedAtUnixMS int64                    `json:"fetchedAtUnixMs"`
	Chunks          []KnowledgeDocumentChunk `json:"chunks"`
}

type KnowledgeIngestFact struct {
	Topic                 string   `json:"topic,omitempty"`
	Subject               string   `json:"subject"`
	Predicate             string   `json:"predicate"`
	Value                 string   `json:"value"`
	Statement             string   `json:"statement"`
	ConfidenceBasisPoints uint16   `json:"confidenceBasisPoints"`
	SourceHitIDs          []string `json:"sourceHitIDs,omitempty"`
	EvidenceChunkIDs      []string `json:"evidenceChunkIDs"`
}
