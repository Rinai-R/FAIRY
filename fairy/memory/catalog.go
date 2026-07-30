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
	Sources               []AssistantSource `json:"sources"`
	CreatedAtUnixMS       int64             `json:"createdAtUnixMs"`
	UpdatedAtUnixMS       int64             `json:"updatedAtUnixMs"`
}

type KnowledgeCatalog struct {
	Candidates []KnowledgeRecord `json:"candidates"`
	Verified   []KnowledgeRecord `json:"verified"`
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

const (
	MaxKnowledgeIngestSourceRank      = 5
	MaxKnowledgeIngestSourceJSONBytes = 16 << 10
	MaxKnowledgeIngestTitleRunes      = 300
	MaxKnowledgeIngestSnippetRunes    = 1200
	MaxKnowledgeSearchCandidates      = 4
)

type KnowledgeIngestSource struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type KnowledgeIngestTask struct {
	ID             string                `json:"id"`
	ConversationID string                `json:"conversationId"`
	TurnID         string                `json:"turnId"`
	Source         KnowledgeIngestSource `json:"source"`
}

type KnowledgeDocument struct {
	SourceID           string `json:"sourceId"`
	CanonicalURL       string `json:"canonicalUrl"`
	Title              string `json:"title"`
	Content            string `json:"content"`
	ContentHash        string `json:"contentHash"`
	EvidenceID         string `json:"evidenceId"`
	ContentType        string `json:"contentType"`
	ETag               string `json:"etag"`
	LastModified       string `json:"lastModified"`
	FetchedAtUnixMS    int64  `json:"fetchedAtUnixMs"`
	ReconcilerRevision string `json:"reconcilerRevision,omitempty"`
}

type KnowledgeMutationOperation string

const (
	KnowledgeMutationAdd    KnowledgeMutationOperation = "ADD"
	KnowledgeMutationUpdate KnowledgeMutationOperation = "UPDATE"
	KnowledgeMutationDelete KnowledgeMutationOperation = "DELETE"
	KnowledgeMutationNone   KnowledgeMutationOperation = "NONE"
)

type KnowledgeDocumentAction struct {
	Operation             KnowledgeMutationOperation `json:"operation"`
	MemoryID              string                     `json:"memoryId,omitempty"`
	Content               string                     `json:"content,omitempty"`
	ConfidenceBasisPoints uint16                     `json:"confidenceBasisPoints,omitempty"`
	Evidence              string                     `json:"evidence"`
}
