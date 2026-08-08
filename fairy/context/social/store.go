package social

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"

	coredb "fairy/runtime/database"

	"github.com/jackc/pgx/v5"
)

var ErrDatabasePoolEmpty = errors.New("social memory database pool is required")

const (
	MaxFTSQueryChars        = 2000
	maxSocialPersonNotes    = 8
	maxSocialQueryFragments = 16
	maxSocialQueryRunes     = 256
	socialQueryWindowRunes  = 4
)

type Store struct {
	pool *coredb.Pool
}

func NewStoreFromPool(pool *coredb.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	return &Store{pool: pool}, nil
}

type scanner interface{ Scan(...any) error }
type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
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

func normalizePostgresSearchQuery(query string) (string, error) {
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
