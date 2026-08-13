package extraction

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
	"fairy/runtime/embedding"
)

var (
	ErrDatabasePoolEmpty         = errors.New("extraction database pool is required")
	ErrSeekDBConnectionEmpty     = errors.New("extraction SeekDB connection is required")
	ErrSeekDBQueryLimitInvalid   = errors.New("extraction SeekDB query limit must be greater than zero")
	ErrWorkerIDInvalid           = errors.New("extraction worker id is invalid")
	ErrJobLeaseInvalid           = errors.New("extraction job lease duration is invalid")
	ErrStoreBackendUnavailable   = errors.New("extraction store backend is unavailable")
	ErrPersonalSettlementPending = errors.New("personal extraction settlement has not been migrated to SeekDB")
)

const defaultJobLeaseDuration = 30 * time.Second

// Store owns exactly one extraction persistence authority. The PostgreSQL
// authority still includes personal-memory settlement during the migration;
// the SeekDB authority intentionally exposes coordination only until personal
// facts and coverage can be committed in the same SeekDB transaction.
type Store struct {
	pool             *coredb.Pool
	seekDB           *sql.DB
	queryLimit       time.Duration
	embedder         *embedding.DynamicSemanticEmbedder
	workerID         string
	jobLeaseDuration time.Duration
	now              func() time.Time
	seekDBWriteHook  func(seekDBWriteStage) error
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
		now:              time.Now,
	}, nil
}

// NewSeekDBStore creates an extraction coordinator whose only authority is
// SeekDB. Personal-memory projection and mutation settlement are deliberately
// not available until those facts share the same SeekDB transaction boundary.
func NewSeekDBStore(database *sql.DB, queryLimit time.Duration, workerID string, leaseDuration time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrSeekDBConnectionEmpty
	}
	if queryLimit <= 0 {
		return nil, ErrSeekDBQueryLimitInvalid
	}
	if err := validateASCIIID("worker_id", workerID); err != nil {
		return nil, ErrWorkerIDInvalid
	}
	if leaseDuration <= 0 || leaseDuration.Milliseconds() <= 0 {
		return nil, ErrJobLeaseInvalid
	}
	return &Store{
		seekDB:           database,
		queryLimit:       queryLimit,
		workerID:         workerID,
		jobLeaseDuration: leaseDuration,
		now:              time.Now,
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
	return validateLegacyID(label, value)
}

func validateSeekDBID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	return nil
}

func validateLegacyID(label, value string) error {
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

func validateASCIIID(label, value string) error {
	if err := validateSeekDBID(label, value); err != nil {
		return err
	}
	for _, character := range value {
		if character > unicode.MaxASCII {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	return nil
}

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
