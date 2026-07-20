package memory

const (
	SemanticDatabaseStatusReady = "ready"
)

// SemanticEmbeddingReadiness reports PostgreSQL embedding queue state.
type SemanticEmbeddingReadiness struct {
	Dimensions     int    `json:"dimensions"`
	DatabaseStatus string `json:"databaseStatus"`
	SemanticStatus string `json:"semanticStatus"`
	Reason         string `json:"reason"`
	PendingJobs    int64  `json:"pendingJobs"`
	RunningJobs    int64  `json:"runningJobs"`
	FailedJobs     int64  `json:"failedJobs"`
	EmbeddedItems  int64  `json:"embeddedItems"`
	VectorRows     int64  `json:"vectorRows"`
}
