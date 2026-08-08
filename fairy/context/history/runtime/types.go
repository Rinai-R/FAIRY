package runtime

type ContextWindowRecord struct {
	ConversationID         string  `json:"conversationId"`
	Lane                   string  `json:"lane"`
	WindowNumber           uint64  `json:"windowNumber"`
	FirstWindowID          string  `json:"firstWindowId"`
	PreviousWindowID       *string `json:"previousWindowId,omitempty"`
	WindowID               string  `json:"windowId"`
	ObservedPrefillTokens  *uint64 `json:"observedPrefillTokens,omitempty"`
	EstimatedPrefillTokens *uint64 `json:"estimatedPrefillTokens,omitempty"`
	LastTrigger            string  `json:"lastTrigger"`
	FailureCount           uint64  `json:"failureCount"`
	PromptWindowRevision   uint64  `json:"promptWindowRevision"`
	UpdatedAtUnixMS        int64   `json:"updatedAtUnixMs"`
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
