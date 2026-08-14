package ledger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrSeekDBConnectionEmpty   = errors.New("observability SeekDB connection is required")
	ErrSeekDBQueryLimitInvalid = errors.New("observability SeekDB query limit must be greater than zero")
	ErrStoreBackendUnavailable = errors.New("observability store backend is unavailable")
)

// Store owns durable execution diagnostics: model usage and tool executions.
// It does not own conversation content or learned memory.
type Store struct {
	seekDB     *sql.DB
	queryLimit time.Duration
	now        func() time.Time
}

// NewSeekDBStore creates an observability ledger whose only authority is SeekDB.
func NewSeekDBStore(database *sql.DB, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrSeekDBConnectionEmpty
	}
	if queryLimit <= 0 {
		return nil, ErrSeekDBQueryLimitInvalid
	}
	return &Store{seekDB: database, queryLimit: queryLimit, now: time.Now}, nil
}

func (s *Store) usesSeekDB() bool { return s != nil && s.seekDB != nil }

func (s *Store) seekDBQueryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

func (s *Store) currentUnixMS() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return max(now().UnixMilli(), int64(1))
}

type scanner interface{ Scan(dest ...any) error }

func validateID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || containsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func nowUnixMS() int64 { return time.Now().UnixMilli() }

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
