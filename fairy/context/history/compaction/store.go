package compaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"fairy/context/history/transcript"
	coredb "fairy/runtime/database"
)

var (
	ErrDatabasePoolEmpty       = errors.New("history compaction database pool is required")
	ErrSeekDBConnectionEmpty   = errors.New("history compaction SeekDB connection is required")
	ErrSeekDBQueryLimitInvalid = errors.New("history compaction SeekDB query limit must be greater than zero")
	ErrStoreBackendUnavailable = errors.New("history compaction store backend is unavailable")
)

// Store owns atomic prompt-window, projection, and tiered-compaction commits.
// Transcript reads and writes are owned by transcript.Store.
type Store struct {
	pool            *coredb.Pool
	seekDB          *sql.DB
	queryLimit      time.Duration
	now             func() time.Time
	seekDBWriteHook func(seekDBWriteStage) error
}

func NewStoreFromPool(pool *coredb.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	return &Store{pool: pool, now: time.Now}, nil
}

// NewSeekDBStore creates the edge compaction Store. It never falls back to
// PostgreSQL when the local SeekDB authority fails.
func NewSeekDBStore(database *sql.DB, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrSeekDBConnectionEmpty
	}
	if queryLimit <= 0 {
		return nil, ErrSeekDBQueryLimitInvalid
	}
	return &Store{seekDB: database, queryLimit: queryLimit, now: time.Now}, nil
}

func nowUnixMS() int64 { return time.Now().UnixMilli() }

func (s *Store) currentUnixMS() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return max(now().UnixMilli(), int64(1))
}

func (s *Store) seekDBQueryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

func (s *Store) usesSeekDB() bool { return s != nil && s.seekDB != nil }

func (s *Store) usesPostgres() bool { return s != nil && s.pool != nil && s.pool.Raw() != nil }

func databaseInt64(label string, value uint64) (int64, error) {
	if value > uint64(1<<63-1) {
		return 0, fmt.Errorf("%s exceeds database integer range", label)
	}
	return int64(value), nil
}

type databaseTranscriptBoundary struct {
	turnSequence    int64
	messageSequence int64
}

func validateTranscriptBoundary(boundary transcript.TranscriptBoundary) (databaseTranscriptBoundary, error) {
	turnSequence, err := databaseInt64("expected transcript turn sequence", boundary.TurnSequence)
	if err != nil {
		return databaseTranscriptBoundary{}, err
	}
	messageSequence, err := databaseInt64("expected transcript message sequence", boundary.MessageSequence)
	if err != nil {
		return databaseTranscriptBoundary{}, err
	}
	return databaseTranscriptBoundary{turnSequence: turnSequence, messageSequence: messageSequence}, nil
}

func nextProjectionRevisions(expectedWindow, expectedProjection int64) (int64, int64, error) {
	if expectedWindow < 1 || expectedProjection < 1 {
		return 0, 0, errors.New("expected prompt projection revisions are required")
	}
	if expectedWindow == math.MaxInt64 || expectedProjection == math.MaxInt64 {
		return 0, 0, errors.New("next prompt projection revision exceeds database integer range")
	}
	return expectedWindow + 1, expectedProjection + 1, nil
}
