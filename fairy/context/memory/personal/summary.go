package personal

type Summary struct {
	Conversations           int64 `json:"conversations"`
	ActiveGlobalMemories    int64 `json:"activeGlobalMemories"`
	ActiveCharacterMemories int64 `json:"activeCharacterMemories"`
	NeedsReviewMemories     int64 `json:"needsReviewMemories"`
	PendingExtractionTurns  int64 `json:"pendingExtractionTurns"`
	RunningBatches          int64 `json:"runningBatches"`
	FailedBatches           int64 `json:"failedBatches"`
	ReadOnly                bool  `json:"readOnly"`
}
