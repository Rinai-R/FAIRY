package api

import (
	"context"

	"fairy/coredb"
)

type databaseStatus struct {
	Ready      bool                 `json:"ready"`
	Mode       string               `json:"mode"`
	Descriptor *coredb.Descriptor   `json:"descriptor,omitempty"`
	Schema     *coredb.SchemaStatus `json:"schema,omitempty"`
	Pool       *coredb.PoolStats    `json:"pool,omitempty"`
	Error      string               `json:"error,omitempty"`
}

type secretKeyStatus struct {
	Ready bool   `json:"ready"`
	Mode  string `json:"mode"`
}

type databaseMetrics struct {
	Available  bool              `json:"available"`
	Pool       *coredb.PoolStats `json:"pool,omitempty"`
	VectorRows int64             `json:"vectorRows"`
}

func (s *Server) infrastructureStatus(ctx context.Context) (databaseStatus, secretKeyStatus) {
	database := databaseStatus{Mode: "production"}
	if s.rt.Database == nil {
		database.Mode = "injected_test_dependency"
		database.Error = "database dependency is not available"
	} else {
		descriptor, err := s.rt.Database.Config().Descriptor()
		if err != nil {
			database.Error = err.Error()
		} else if err := s.rt.Database.Ping(ctx); err != nil {
			database.Descriptor = &descriptor
			database.Error = err.Error()
		} else {
			schema, err := coredb.VerifySchema(ctx, s.rt.Database.Raw())
			database.Descriptor = &descriptor
			if err != nil {
				database.Error = err.Error()
			} else {
				stats := s.rt.Database.Stats()
				database.Ready = true
				database.Schema = &schema
				database.Pool = &stats
			}
		}
	}

	secretKey := secretKeyStatus{Ready: s.rt.Secret != nil && s.rt.Secret.Encrypted(), Mode: "production"}
	if s.rt.Database == nil {
		secretKey.Mode = "injected_test_dependency"
	}
	return database, secretKey
}

func (s *Server) infrastructureMetrics(ctx context.Context) (databaseMetrics, error) {
	database := databaseMetrics{}
	if s.rt.Database != nil {
		semantic, err := s.rt.Memory.SemanticEmbeddingStatus()
		if err != nil {
			return databaseMetrics{}, err
		}
		stats := s.rt.Database.Stats()
		database = databaseMetrics{Available: true, Pool: &stats, VectorRows: semantic.VectorRows}
	}
	return database, nil
}
