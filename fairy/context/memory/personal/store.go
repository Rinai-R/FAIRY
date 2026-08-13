package personal

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	coredb "fairy/runtime/database"
	"fairy/runtime/embedding"
)

var (
	ErrDatabasePoolEmpty       = errors.New("personal memory database pool is required")
	ErrSeekDBConnectionEmpty   = errors.New("personal memory SeekDB connection is required")
	ErrSeekDBQueryLimitInvalid = errors.New("personal memory SeekDB query limit must be greater than zero")
	ErrSeekDBTransactionEmpty  = errors.New("personal memory SeekDB transaction is required")
	ErrStoreBackendUnavailable = errors.New("personal memory store backend is unavailable")
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

// NewSeekDBStore creates a personal-memory repository whose only authority is
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

func (s *Store) embeddingForContent(content string) (embedding.EmbeddingValue, error) {
	if s == nil || s.embedder == nil {
		return embedding.EmbeddingValue{}, ErrDatabasePoolEmpty
	}
	return embedding.ForContent(s.embedder, content)
}

// PrepareEmbedding performs the optional provider call outside any database
// transaction. SeekDB-specific tuple encoding remains private to this package.
func (s *Store) PrepareEmbedding(content string) (embedding.EmbeddingValue, error) {
	values, err := s.PrepareEmbeddings([]string{content})
	if err != nil {
		return embedding.EmbeddingValue{}, err
	}
	return values[0], nil
}

// PrepareEmbeddings snapshots the current semantic provider once so every
// value prepared for one settlement belongs to the same embedding space.
func (s *Store) PrepareEmbeddings(contents []string) ([]embedding.EmbeddingValue, error) {
	if s == nil || s.embedder == nil {
		return nil, ErrStoreBackendUnavailable
	}
	return embedding.ForContents(s.semanticEmbedderSnapshot(), contents)
}

func (s *Store) PrepareEmbeddingsContext(
	ctx context.Context,
	contents []string,
) ([]embedding.EmbeddingValue, error) {
	if s == nil || s.embedder == nil {
		return nil, ErrStoreBackendUnavailable
	}
	return embedding.ForContentsContext(ctx, s.semanticEmbedderSnapshot(), contents)
}

// OwnsSeekDB reports whether database is the exact authority supplied to this
// Store. Composition uses it to reject cross-database settlement wiring.
func (s *Store) OwnsSeekDB(database *sql.DB) bool {
	return database != nil && s != nil && s.seekDB == database
}

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

func (s *Store) usesSeekDB() bool { return s != nil && s.seekDB != nil }

func (s *Store) usesPostgres() bool { return s != nil && s.pool != nil && s.pool.Raw() != nil }

func (s *Store) currentUnixMS() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return max(now().UnixMilli(), int64(1))
}

func validateID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	return nil
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
