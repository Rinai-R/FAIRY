package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	coredb "fairy/runtime/database"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrDatabasePoolEmpty = errors.New("observability database pool is required")

// Store owns durable execution diagnostics: model usage and tool executions.
// It does not own conversation content or learned memory.
type Store struct {
	pool *coredb.Pool
}

func NewStoreFromPool(pool *coredb.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	return &Store{pool: pool}, nil
}

type scanner interface{ Scan(dest ...any) error }
type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func validateID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || containsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func stringPtrFromPGText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
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
