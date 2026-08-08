// Package personal defines the personal-memory model shared by retrieval,
// manual curation, and asynchronous extraction.
package personal

const MaxContentRunes = 2400

type Scope struct {
	Type        string `json:"type"`
	CharacterID string `json:"characterId,omitempty"`
}

type Record struct {
	ID                    string  `json:"id"`
	Kind                  string  `json:"kind"`
	Scope                 Scope   `json:"scope"`
	ReviewStatus          string  `json:"reviewStatus"`
	Content               string  `json:"content"`
	Status                string  `json:"status"`
	ConfidenceBasisPoints uint16  `json:"confidenceBasisPoints"`
	SourceConversationID  string  `json:"sourceConversationId"`
	SourceTurnID          string  `json:"sourceTurnId"`
	SupersedesID          *string `json:"supersedesId"`
	CreatedAtUnixMS       int64   `json:"createdAtUnixMs"`
	UpdatedAtUnixMS       int64   `json:"updatedAtUnixMs"`
}

type Catalog struct {
	Global      []Record `json:"global"`
	Character   []Record `json:"character"`
	NeedsReview []Record `json:"needsReview"`
}

type Retrieved struct {
	ID                    string  `json:"id"`
	Kind                  string  `json:"kind"`
	Layer                 string  `json:"layer"`
	Scope                 Scope   `json:"scope"`
	Content               string  `json:"content"`
	ConfidenceBasisPoints uint16  `json:"confidenceBasisPoints"`
	UpdatedAtUnixMS       int64   `json:"updatedAtUnixMs"`
	TextScore             float64 `json:"-"`
}

type Retrieval struct {
	PersonalMemories []Retrieved `json:"personalMemories"`
	SemanticStatus   string      `json:"semanticStatus,omitempty"`
}

func (r Retrieval) Empty() bool { return len(r.PersonalMemories) == 0 }

const (
	maxPortraitMemories   = 6
	maxPortraitPerKind    = 2
	maxPortraitRunes      = 1200
	maxPortraitCandidates = 16
)
