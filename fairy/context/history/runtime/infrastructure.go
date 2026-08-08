package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	coredb "fairy/runtime/database"

	"github.com/jackc/pgx/v5"
)

var ErrDatabasePoolEmpty = errors.New("history runtime database pool is required")

type Store struct {
	pool *coredb.Pool
}

func NewStoreFromPool(pool *coredb.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	return &Store{pool: pool}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

type RowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

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

func validateID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > 128 || containsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
