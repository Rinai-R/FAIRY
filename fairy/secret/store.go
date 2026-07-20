package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	pgstore "fairy/postgres"

	"github.com/jackc/pgx/v5"
)

var (
	ErrConnectionIDRequired = errors.New("model connection_id is required")
	ErrInvalidConnectionID  = errors.New("model connection_id must not contain leading or trailing whitespace")
	ErrSecretRequired       = errors.New("model credential is required")
	ErrInvalidSecret        = errors.New("model credential must not contain leading or trailing whitespace")
	ErrDatabasePoolRequired = errors.New("secret database pool is required")
)

// Value stores an exact secret value in memory. It deliberately redacts fmt and
// JSON output so callers cannot accidentally echo API keys into DTOs or logs.
type Value struct {
	raw string
}

// NewValue validates an exact secret value. It rejects, rather than trims,
// leading or trailing whitespace because credentials are exact-match values.
func NewValue(raw string) (Value, error) {
	if raw == "" {
		return Value{}, ErrSecretRequired
	}
	if strings.TrimSpace(raw) != raw {
		return Value{}, ErrInvalidSecret
	}
	return Value{raw: raw}, nil
}

// Expose returns the raw credential for the narrow boundary that constructs an
// Authorization header. Do not pass this value to DTOs, logs, JSON, or errors.
func (v Value) Expose() string {
	return v.raw
}

func (v Value) String() string {
	return "[REDACTED]"
}

func (v Value) GoString() string {
	return "secret.Value([REDACTED])"
}

func (v Value) MarshalJSON() ([]byte, error) {
	return nil, errors.New("secret value cannot be JSON encoded")
}

// Store persists encrypted production secrets in PostgreSQL. Unit tests may
// opt into the explicit in-memory store returned by NewTestStore.
type Store struct {
	pool       *pgstore.Pool
	cipher     *Cipher
	now        func() time.Time
	testMu     sync.RWMutex
	testValues map[string]Value
}

func NewPostgresStore(pool *pgstore.Pool, cipher *Cipher) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolRequired
	}
	if cipher == nil || cipher.aead == nil {
		return nil, ErrCipherRequired
	}
	return &Store{pool: pool, cipher: cipher, now: time.Now}, nil
}

// NewTestStore returns an explicit in-memory store for unit tests. Production
// composition must use NewPostgresStore.
func NewTestStore() *Store {
	return &Store{now: time.Now, testValues: make(map[string]Value)}
}

// Encrypted reports whether the store is backed by PostgreSQL and has an
// initialized AEAD cipher. It never exposes key material.
func (s *Store) Encrypted() bool {
	return s != nil && s.pool != nil && s.pool.Raw() != nil && s.cipher != nil && s.cipher.aead != nil
}

func (s *Store) Save(connectionID string, value Value) error {
	return s.SaveContext(context.Background(), connectionID, value)
}

func (s *Store) SaveContext(ctx context.Context, connectionID string, value Value) error {
	if err := validateConnectionID(connectionID); err != nil {
		return err
	}
	if _, err := NewValue(value.raw); err != nil {
		return err
	}
	if s != nil && s.testValues != nil {
		s.testMu.Lock()
		s.testValues[connectionID] = value
		s.testMu.Unlock()
		return nil
	}
	if s == nil || s.pool == nil {
		return ErrDatabasePoolRequired
	}
	return s.savePostgres(ctx, connectionID, value)
}

func (s *Store) Load(connectionID string) (Value, bool, error) {
	return s.LoadContext(context.Background(), connectionID)
}

func (s *Store) LoadContext(ctx context.Context, connectionID string) (Value, bool, error) {
	if err := validateConnectionID(connectionID); err != nil {
		return Value{}, false, err
	}
	if s != nil && s.testValues != nil {
		s.testMu.RLock()
		value, ok := s.testValues[connectionID]
		s.testMu.RUnlock()
		return value, ok, nil
	}
	if s == nil || s.pool == nil {
		return Value{}, false, ErrDatabasePoolRequired
	}
	return s.loadPostgres(ctx, connectionID)
}

func (s *Store) Delete(connectionID string) error {
	return s.DeleteContext(context.Background(), connectionID)
}

func (s *Store) DeleteContext(ctx context.Context, connectionID string) error {
	if err := validateConnectionID(connectionID); err != nil {
		return err
	}
	if s != nil && s.testValues != nil {
		s.testMu.Lock()
		delete(s.testValues, connectionID)
		s.testMu.Unlock()
		return nil
	}
	if s == nil || s.pool == nil {
		return ErrDatabasePoolRequired
	}
	return s.deletePostgres(ctx, connectionID)
}

func (s *Store) savePostgres(ctx context.Context, name string, value Value) error {
	if s.cipher == nil {
		return ErrCipherRequired
	}
	namespace := secretNamespace(name)
	plaintext := []byte(value.raw)
	nonce, ciphertext, aad, err := s.cipher.Seal(namespace, name, plaintext)
	clear(plaintext)
	if err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	_, err = s.pool.Raw().Exec(queryCtx, `
INSERT INTO secret_values(namespace, name, key_version, nonce, ciphertext, aad, created_at_ms, updated_at_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT(namespace, name) DO UPDATE SET
  key_version = excluded.key_version,
  nonce = excluded.nonce,
  ciphertext = excluded.ciphertext,
  aad = excluded.aad,
  updated_at_ms = excluded.updated_at_ms`, namespace, name, KeyVersion, nonce, ciphertext, aad, s.currentUnixMillis())
	clear(ciphertext)
	if err != nil {
		return fmt.Errorf("saving encrypted secret: %w", err)
	}
	return nil
}

func (s *Store) loadPostgres(ctx context.Context, name string) (Value, bool, error) {
	if s.cipher == nil {
		return Value{}, false, ErrCipherRequired
	}
	namespace := secretNamespace(name)
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var keyVersion int
	var nonce, ciphertext []byte
	var aad string
	err := s.pool.Raw().QueryRow(queryCtx, `
SELECT key_version, nonce, ciphertext, aad
FROM secret_values
WHERE namespace = $1 AND name = $2`, namespace, name).Scan(&keyVersion, &nonce, &ciphertext, &aad)
	if errors.Is(err, pgx.ErrNoRows) {
		return Value{}, false, nil
	}
	if err != nil {
		return Value{}, false, fmt.Errorf("loading encrypted secret: %w", err)
	}
	plaintext, err := s.cipher.Open(namespace, name, keyVersion, nonce, ciphertext, aad)
	clear(ciphertext)
	if err != nil {
		return Value{}, false, err
	}
	raw := string(plaintext)
	clear(plaintext)
	value, err := NewValue(raw)
	if err != nil {
		return Value{}, false, errors.New("decrypted secret value is invalid")
	}
	return value, true, nil
}

func (s *Store) deletePostgres(ctx context.Context, name string) error {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	if _, err := s.pool.Raw().Exec(queryCtx, "DELETE FROM secret_values WHERE namespace = $1 AND name = $2", secretNamespace(name), name); err != nil {
		return fmt.Errorf("deleting encrypted secret: %w", err)
	}
	return nil
}

func secretNamespace(name string) string {
	if strings.HasPrefix(name, "speech.") {
		return "speech"
	}
	return "model"
}

func (s *Store) currentUnixMillis() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return now().UnixMilli()
}

func validateConnectionID(connectionID string) error {
	if connectionID == "" {
		return ErrConnectionIDRequired
	}
	if strings.TrimSpace(connectionID) != connectionID {
		return ErrInvalidConnectionID
	}
	return nil
}

var _ json.Marshaler = Value{}
