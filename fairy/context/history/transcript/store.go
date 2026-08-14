package transcript

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

var (
	ErrSeekDBConnectionEmpty   = errors.New("history SeekDB connection is required")
	ErrSeekDBQueryLimitInvalid = errors.New("history SeekDB query limit must be greater than zero")
	ErrStoreBackendUnavailable = errors.New("history store backend is unavailable")
)

// Store owns durable conversations, turns, messages, prompt windows and
// continuation state. It intentionally has no semantic embedder or learning
// worker because those belong to the memory and knowledge domains.
type Store struct {
	seekDB         *sql.DB
	queryLimit     time.Duration
	now            func() time.Time
	seekDBTurnHook func(seekDBTurnWriteStage) error
}

// NewSeekDBStore creates the edge transcript repository.
func NewSeekDBStore(database *sql.DB, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrSeekDBConnectionEmpty
	}
	if queryLimit <= 0 {
		return nil, ErrSeekDBQueryLimitInvalid
	}
	return &Store{seekDB: database, queryLimit: queryLimit, now: time.Now}, nil
}

type scanner interface {
	Scan(dest ...any) error
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

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(data[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
