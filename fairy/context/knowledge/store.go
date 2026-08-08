package knowledge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	coredb "fairy/runtime/database"
	"fairy/runtime/embedding"

	"github.com/jackc/pgx/v5"
)

var ErrDatabasePoolEmpty = errors.New("knowledge database pool is required")

type Store struct {
	pool     *coredb.Pool
	embedder *embedding.DynamicSemanticEmbedder
}

func NewStoreFromPool(pool *coredb.Pool, embedder embedding.SemanticEmbedder) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	return &Store{pool: pool, embedder: embedding.NewDynamicSemanticEmbedder(embedder)}, nil
}

func (s *Store) ReplaceSemanticEmbedder(embedder embedding.SemanticEmbedder) {
	if s != nil && s.embedder != nil {
		s.embedder.Replace(embedder)
	}
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
