package knowledge

type AssistantSource struct {
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type Record struct {
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

type Catalog struct {
	Candidates []Record `json:"candidates"`
	Verified   []Record `json:"verified"`
}

type Retrieved struct {
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

// Retrieval is the bounded, verified public-knowledge view returned to agents.
type Retrieval struct {
	Entries        []Retrieved `json:"entries"`
	SemanticStatus string      `json:"semanticStatus,omitempty"`
}

const (
	MaxIngestSourceRank      = 5
	MaxIngestSourceJSONBytes = 16 << 10
	MaxIngestTitleRunes      = 300
	MaxIngestSnippetRunes    = 1200
	MaxSearchCandidates      = 4
)

type IngestSource struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type IngestTask struct {
	ID             string       `json:"id"`
	ConversationID string       `json:"conversationId"`
	TurnID         string       `json:"turnId"`
	Source         IngestSource `json:"source"`
}

type Document struct {
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

type MutationOperation string

const (
	MutationAdd    MutationOperation = "ADD"
	MutationUpdate MutationOperation = "UPDATE"
	MutationDelete MutationOperation = "DELETE"
	MutationNone   MutationOperation = "NONE"
)

type DocumentAction struct {
	Operation             MutationOperation `json:"operation"`
	MemoryID              string            `json:"memoryId,omitempty"`
	Content               string            `json:"content,omitempty"`
	ConfidenceBasisPoints uint16            `json:"confidenceBasisPoints,omitempty"`
	Evidence              string            `json:"evidence"`
}
