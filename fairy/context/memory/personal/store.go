package personal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	coredb "fairy/runtime/database"
	"fairy/runtime/embedding"
)

var ErrDatabasePoolEmpty = errors.New("personal memory database pool is required")

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

func (s *Store) embeddingForContent(content string) (embedding.EmbeddingValue, error) {
	if s == nil || s.embedder == nil {
		return embedding.EmbeddingValue{}, ErrDatabasePoolEmpty
	}
	return embedding.ForContent(s.embedder, content)
}

func (s *Store) semanticEmbedderSnapshot() embedding.SemanticEmbedder {
	if s == nil || s.embedder == nil {
		return nil
	}
	return s.embedder.Snapshot()
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
