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

	"fairy/context/memory/personal"
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
	ErrPersonalStoreEmpty        = errors.New("extraction personal memory store is required")
	ErrPersonalAuthorityMismatch = errors.New("extraction and personal memory stores must share one SeekDB authority")
	ErrExtractionClaimConflict   = errors.New("extraction claim changed before settlement")
)

const defaultJobLeaseDuration = 30 * time.Second

// Store owns exactly one extraction persistence authority. The PostgreSQL
// authority remains available for migration parity; a SeekDB Store becomes a
// full settlement authority only when composed with a personal Store over the
// same *sql.DB. The coordinator-only constructor fails closed for personal
// projection, settlement, and coverage APIs.
type Store struct {
	pool             *coredb.Pool
	seekDB           *sql.DB
	personal         *personal.Store
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

// NewSeekDBStore creates a coordinator whose only authority is SeekDB. Use
// NewSeekDBStoreWithPersonal when personal facts and coverage must participate
// in the same transaction.
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

// NewSeekDBStoreWithPersonal creates a full extraction Store whose queue,
// personal facts, evidence and coverage share one SeekDB authority. The
// coordinator-only constructor remains available for task 3.5 callers.
func NewSeekDBStoreWithPersonal(
	database *sql.DB,
	queryLimit time.Duration,
	workerID string,
	leaseDuration time.Duration,
	personalStore *personal.Store,
) (*Store, error) {
	store, err := NewSeekDBStore(database, queryLimit, workerID, leaseDuration)
	if err != nil {
		return nil, err
	}
	if personalStore == nil {
		return nil, ErrPersonalStoreEmpty
	}
	if !personalStore.OwnsSeekDB(database) {
		return nil, ErrPersonalAuthorityMismatch
	}
	store.personal = personalStore
	return store, nil
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
