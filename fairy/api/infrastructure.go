package api

import (
	"context"

	"fairy/coredb"
	"fairy/memory"
)

type databaseStatus struct {
	Ready      bool                 `json:"ready"`
	Mode       string               `json:"mode"`
	Descriptor *coredb.Descriptor   `json:"descriptor,omitempty"`
	Schema     *coredb.SchemaStatus `json:"schema,omitempty"`
	Pool       *coredb.PoolStats    `json:"pool,omitempty"`
	Error      string               `json:"error,omitempty"`
}

type qdrantStatus struct {
	Ready      bool                           `json:"ready"`
	Mode       string                         `json:"mode"`
	Descriptor *memory.VectorDescriptor       `json:"descriptor,omitempty"`
	Collection *memory.VectorCollectionStatus `json:"collection,omitempty"`
	Error      string                         `json:"error,omitempty"`
}

type secretKeyStatus struct {
	Ready bool   `json:"ready"`
	Mode  string `json:"mode"`
}

type databaseMetrics struct {
	Available bool                  `json:"available"`
	Pool      *coredb.PoolStats     `json:"pool,omitempty"`
	Vector    *memory.VectorMetrics `json:"vector,omitempty"`
}

type qdrantMetrics struct {
	Available bool                          `json:"available"`
	Snapshot  *memory.VectorMetricsSnapshot `json:"snapshot,omitempty"`
}

func (s *Server) infrastructureStatus(ctx context.Context) (databaseStatus, qdrantStatus, secretKeyStatus) {
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

	qdrant := qdrantStatus{Mode: "production"}
	if s.rt.VectorIndex == nil {
		qdrant.Mode = "injected_test_dependency"
		qdrant.Error = "qdrant dependency is not available"
	} else {
		descriptor, err := s.rt.VectorIndex.Descriptor()
		if err != nil {
			qdrant.Error = err.Error()
		} else {
			qdrant.Descriptor = &descriptor
			collection, err := s.rt.VectorIndex.VerifyCollection(ctx)
			if err != nil {
				qdrant.Error = err.Error()
			} else {
				qdrant.Ready = true
				qdrant.Collection = &collection
			}
		}
	}

	secretKey := secretKeyStatus{Ready: s.rt.Secret != nil && s.rt.Secret.Encrypted(), Mode: "production"}
	if s.rt.Database == nil {
		secretKey.Mode = "injected_test_dependency"
	}
	return database, qdrant, secretKey
}

func (s *Server) infrastructureMetrics(ctx context.Context) (databaseMetrics, qdrantMetrics, error) {
	database := databaseMetrics{}
	if s.rt.Database != nil {
		vectorMetrics, err := s.rt.MemoryStore.VectorMetricsContext(ctx)
		if err != nil {
			return databaseMetrics{}, qdrantMetrics{}, err
		}
		stats := s.rt.Database.Stats()
		database = databaseMetrics{Available: true, Pool: &stats, Vector: &vectorMetrics}
	}
	qdrant := qdrantMetrics{}
	if s.rt.VectorIndex != nil {
		snapshot, err := s.rt.VectorIndex.Metrics(ctx)
		if err != nil {
			return databaseMetrics{}, qdrantMetrics{}, err
		}
		qdrant = qdrantMetrics{Available: true, Snapshot: &snapshot}
	}
	return database, qdrant, nil
}
