package social

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
)

var (
	ErrSeekDBConnectionEmpty   = errors.New("social memory SeekDB connection is required")
	ErrSeekDBQueryLimitInvalid = errors.New("social memory SeekDB query limit must be greater than zero")
	ErrStoreBackendUnavailable = errors.New("social memory store backend is unavailable")
)

const (
	MaxFTSQueryChars        = 2000
	maxSocialPersonNotes    = 8
	maxSocialQueryFragments = 16
	maxSocialQueryRunes     = 256
	socialQueryWindowRunes  = 4
)

type Store struct {
	seekDB     *sql.DB
	queryLimit time.Duration
	now        func() time.Time
}

// NewSeekDBStore creates a social memory repository whose only authority is SeekDB.
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

type scanner interface{ Scan(...any) error }

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

func ValidateID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || len([]rune(value)) > 128 {
		return errors.New(label + " is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New(label + " is invalid")
		}
	}
	return nil
}

func normalizeSearchQuery(query string) (string, error) {
	if len([]rune(query)) > maxSocialQueryRunes {
		return "", errors.New("social retrieval query is too long or contains control characters")
	}
	hasUsableRun := false
	runLength := 0
	for _, character := range query {
		if unicode.IsControl(character) {
			return "", errors.New("social retrieval query is too long or contains control characters")
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			runLength++
			if runLength >= 3 {
				hasUsableRun = true
			}
		} else {
			runLength = 0
		}
	}
	if !hasUsableRun {
		return "", nil
	}
	return strings.Join(strings.Fields(query), " "), nil
}
