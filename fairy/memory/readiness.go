package memory

const SemanticDatabaseStatusReady = "ready"

// SemanticEmbeddingReadiness reports PostgreSQL vector projection state.
type SemanticEmbeddingReadiness struct {
	Dimensions     int    `json:"dimensions"`
	DatabaseStatus string `json:"databaseStatus"`
	SemanticStatus string `json:"semanticStatus"`
	Reason         string `json:"reason"`
	VectorRows     int64  `json:"vectorRows"`
}
