package compaction

import (
	"errors"
	"fmt"
	"time"

	coredb "fairy/runtime/database"
)

var ErrDatabasePoolEmpty = errors.New("history compaction database pool is required")

// Store owns atomic prompt-window, projection, and tiered-compaction commits.
// Transcript reads and writes are owned by transcript.Store.
type Store struct {
	pool *coredb.Pool
}

func NewStoreFromPool(pool *coredb.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	return &Store{pool: pool}, nil
}

func nowUnixMS() int64 { return time.Now().UnixMilli() }

func databaseInt64(label string, value uint64) (int64, error) {
	if value > uint64(1<<63-1) {
		return 0, fmt.Errorf("%s exceeds database integer range", label)
	}
	return int64(value), nil
}
