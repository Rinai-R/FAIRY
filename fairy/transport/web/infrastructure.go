package web

import "context"

type databaseStatus struct {
	Ready      bool   `json:"ready"`
	Mode       string `json:"mode"`
	Storage    string `json:"storage,omitempty"`
	Descriptor any    `json:"descriptor,omitempty"`
	Schema     any    `json:"schema,omitempty"`
	Error      string `json:"error,omitempty"`
}

type secretKeyStatus struct {
	Ready bool   `json:"ready"`
	Mode  string `json:"mode"`
}

type databaseMetrics struct {
	Available       bool  `json:"available"`
	OpenConnections int   `json:"openConnections,omitempty"`
	VectorRows      int64 `json:"vectorRows"`
}

func (s *Server) infrastructureStatus(ctx context.Context) (databaseStatus, secretKeyStatus) {
	secretKey := secretKeyStatus{Ready: s.rt.Secret != nil && s.rt.Secret.Encrypted(), Mode: "production"}
	if s.rt.QueryStorageStatus == nil {
		return databaseStatus{Mode: "unavailable", Error: "storage status is not available"}, secretKey
	}
	status, err := s.rt.QueryStorageStatus(ctx)
	if err != nil {
		return databaseStatus{Mode: "production", Storage: "seekdb", Error: err.Error()}, secretKey
	}
	return databaseStatus{
		Ready:      status.Ready,
		Mode:       status.Mode,
		Storage:    status.Storage,
		Descriptor: status.Descriptor,
		Schema:     status.Schema,
		Error:      status.Error,
	}, secretKey
}

func (s *Server) infrastructureMetrics(ctx context.Context) (databaseMetrics, error) {
	database := databaseMetrics{}
	if s.rt.QueryStorageStatus == nil || s.rt.Memory == nil || s.rt.KnowledgeStore == nil {
		return database, nil
	}
	status, err := s.rt.QueryStorageStatus(ctx)
	if err != nil {
		return databaseMetrics{}, err
	}
	if !status.Ready {
		return database, nil
	}
	semantic, err := s.rt.Memory.SemanticEmbeddingStatus()
	if err != nil {
		return databaseMetrics{}, err
	}
	knowledgeStats, err := s.rt.KnowledgeStore.StatsContext(ctx)
	if err != nil {
		return databaseMetrics{}, err
	}
	return databaseMetrics{
		Available:       true,
		OpenConnections: status.OpenConnections,
		VectorRows:      semantic.VectorRows + knowledgeStats.VectorRows,
	}, nil
}
