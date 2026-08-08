package extraction

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

var (
	ErrDatabasePoolEmpty = errors.New("extraction database pool is required")
	ErrWorkerIDInvalid   = errors.New("extraction worker id is invalid")
	ErrJobLeaseInvalid   = errors.New("extraction job lease duration is invalid")
)

const defaultJobLeaseDuration = 30 * time.Second

// Store owns the asynchronous extraction queue, leases, committed coverage,
// and the atomic application of model-produced personal-memory mutations.
type Store struct {
	pool             *coredb.Pool
	embedder         *embedding.DynamicSemanticEmbedder
	workerID         string
	jobLeaseDuration time.Duration
}

func NewStoreFromPool(pool *coredb.Pool, embedder embedding.SemanticEmbedder) (*Store, error) {
	return NewStoreFromPoolWithLease(pool, embedder, "extraction-"+newID(), defaultJobLeaseDuration)
}

func NewStoreFromPoolWithLease(pool *coredb.Pool, embedder embedding.SemanticEmbedder, workerID string, leaseDuration time.Duration) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	if err := validateID("worker_id", workerID); err != nil {
		return nil, ErrWorkerIDInvalid
	}
	if leaseDuration <= 0 {
		return nil, ErrJobLeaseInvalid
	}
	return &Store{
		pool:             pool,
		embedder:         embedding.NewDynamicSemanticEmbedder(embedder),
		workerID:         workerID,
		jobLeaseDuration: leaseDuration,
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
