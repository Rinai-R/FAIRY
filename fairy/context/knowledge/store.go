package knowledge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	coredb "fairy/runtime/database"
	"fairy/runtime/embedding"

	"github.com/jackc/pgx/v5"
)

var (
	ErrDatabasePoolEmpty       = errors.New("knowledge database pool is required")
	ErrSeekDBConnectionEmpty   = errors.New("knowledge SeekDB connection is required")
	ErrSeekDBQueryLimitInvalid = errors.New("knowledge SeekDB query limit must be greater than zero")
	ErrStoreBackendUnavailable = errors.New("knowledge store backend is unavailable")
)

type Store struct {
	pool       *coredb.Pool
	seekDB     *sql.DB
	queryLimit time.Duration
	embedder   *embedding.DynamicSemanticEmbedder
	now        func() time.Time
}

func NewStoreFromPool(pool *coredb.Pool, embedder embedding.SemanticEmbedder) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	return &Store{pool: pool, embedder: embedding.NewDynamicSemanticEmbedder(embedder), now: time.Now}, nil
}

// NewSeekDBStore creates a knowledge repository whose only authority is
// SeekDB. It never falls back to the legacy PostgreSQL pool.
func NewSeekDBStore(database *sql.DB, queryLimit time.Duration, embedder embedding.SemanticEmbedder) (*Store, error) {
	if database == nil {
		return nil, ErrSeekDBConnectionEmpty
	}
	if queryLimit <= 0 {
		return nil, ErrSeekDBQueryLimitInvalid
	}
	return &Store{
		seekDB: database, queryLimit: queryLimit,
		embedder: embedding.NewDynamicSemanticEmbedder(embedder), now: time.Now,
	}, nil
}

func (s *Store) ReplaceSemanticEmbedder(embedder embedding.SemanticEmbedder) {
	if s != nil && s.embedder != nil {
		s.embedder.Replace(embedder)
	}
}

func (s *Store) usesSeekDB() bool { return s != nil && s.seekDB != nil }

func (s *Store) usesPostgres() bool { return s != nil && s.pool != nil && s.pool.Raw() != nil }

func (s *Store) semanticEmbedderSnapshot() embedding.SemanticEmbedder {
	if s == nil || s.embedder == nil {
		return nil
	}
	return s.embedder.Snapshot()
}

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
type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
type DatabaseQuerier interface {
	Querier
	QueryRow(context.Context, string, ...any) pgx.Row
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
