// Package memory owns FAIRY's conversation and long-term memory store facade.
//
// Domain facts, validation, and pure projections live in
// fairy/internal/domain/memory. The concrete PostgreSQL Store lives in
// fairy/internal/adapters/memory/postgres; this package re-exports it for
// stable consumer imports.
package memory

import (
	"time"

	mempostgres "fairy/internal/adapters/memory/postgres"
	pgstore "fairy/postgres"
)

const (
	SemanticEmbeddingModelID    = mempostgres.SemanticEmbeddingModelID
	SemanticEmbeddingDimensions = mempostgres.SemanticEmbeddingDimensions
	DefaultUsageTurnLimit       = mempostgres.DefaultUsageTurnLimit
)

var (
	ErrDatabasePoolEmpty       = mempostgres.ErrDatabasePoolEmpty
	ErrWorkerIDInvalid         = mempostgres.ErrWorkerIDInvalid
	ErrJobLeaseInvalid         = mempostgres.ErrJobLeaseInvalid
	ErrEndpointBindingMismatch = mempostgres.ErrEndpointBindingMismatch
)

type Store = mempostgres.Store
type Summary = mempostgres.Summary

func NewStoreFromPool(pool *pgstore.Pool) (*Store, error) {
	return mempostgres.NewStoreFromPool(pool)
}

func newStoreFromPoolWithLease(pool *pgstore.Pool, workerID string, leaseDuration time.Duration) (*Store, error) {
	return mempostgres.NewStoreFromPoolWithLease(pool, workerID, leaseDuration)
}
