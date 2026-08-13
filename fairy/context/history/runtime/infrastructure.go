package runtime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	coredb "fairy/runtime/database"

	"github.com/jackc/pgx/v5"
)

var (
	ErrDatabasePoolEmpty       = errors.New("history runtime database pool is required")
	ErrSeekDBConnectionEmpty   = errors.New("history runtime SeekDB connection is required")
	ErrSeekDBQueryLimitInvalid = errors.New("history runtime SeekDB query limit must be greater than zero")
	ErrStoreBackendUnavailable = errors.New("history runtime store backend is unavailable")
)

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

// NewSeekDBStore creates the edge runtime-state store. SeekDB is the only
// authority for this Store; connector failures are returned without fallback.
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

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
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

func validateID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > 128 || containsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
