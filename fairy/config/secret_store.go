package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fairy/coredb"
	"fairy/session"

	"github.com/jackc/pgx/v5"
)

var (
	ErrConnectionIDRequired       = errors.New("model connection_id is required")
	ErrInvalidConnectionID        = errors.New("model connection_id must not contain leading or trailing whitespace")
	ErrSecretRequired             = errors.New("model credential is required")
	ErrInvalidSecret              = errors.New("model credential must not contain leading or trailing whitespace")
	ErrSecretDatabasePoolRequired = errors.New("secret database pool is required")
)

// SecretValue stores an exact secret value in memory. It deliberately redacts fmt and
// JSON output so callers cannot accidentally echo API keys into DTOs or logs.
type SecretValue struct {
	raw string
}

// NewSecretValue validates an exact secret value. It rejects, rather than trims,
// leading or trailing whitespace because credentials are exact-match values.
func NewSecretValue(raw string) (SecretValue, error) {
	if raw == "" {
		return SecretValue{}, ErrSecretRequired
	}
	if strings.TrimSpace(raw) != raw {
		return SecretValue{}, ErrInvalidSecret
	}
	return SecretValue{raw: raw}, nil
}

// Expose returns the raw credential for the narrow boundary that constructs an
// Authorization header. Do not pass this value to DTOs, logs, JSON, or errors.
func (v SecretValue) Expose() string {
	return v.raw
}

func (v SecretValue) String() string {
	return "[REDACTED]"
}

func (v SecretValue) GoString() string {
	return "config.SecretValue([REDACTED])"
}

func (v SecretValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("secret value cannot be JSON encoded")
}

// SecretStore persists encrypted production secrets in PostgreSQL. Unit tests may
// opt into the explicit in-memory store returned by NewTestSecretStore.
type SecretStore struct {
	pool       *coredb.Pool
	cipher     *SecretCipher
	now        func() time.Time
	testMu     sync.RWMutex
	testValues map[string]SecretValue
}

func NewPostgresSecretStore(pool *coredb.Pool, cipher *SecretCipher) (*SecretStore, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrSecretDatabasePoolRequired
	}
	if cipher == nil || cipher.aead == nil {
		return nil, ErrSecretCipherRequired
	}
	return &SecretStore{pool: pool, cipher: cipher, now: time.Now}, nil
}

// NewTestSecretStore returns an explicit in-memory store for unit tests. Production
// composition must use NewPostgresSecretStore.
func NewTestSecretStore() *SecretStore {
	return &SecretStore{now: time.Now, testValues: make(map[string]SecretValue)}
}

// Encrypted reports whether the store is backed by PostgreSQL and has an
// initialized AEAD cipher. It never exposes key material.
func (s *SecretStore) Encrypted() bool {
	return s != nil && s.pool != nil && s.pool.Raw() != nil && s.cipher != nil && s.cipher.aead != nil
}

func (s *SecretStore) DigestEndpointKey(endpoint session.EndpointKind, rawKey string) (string, error) {
	if s == nil || s.cipher == nil {
		return "", ErrSecretCipherRequired
	}
	return s.cipher.DigestEndpointKey(endpoint, rawKey)
}

func (s *SecretStore) DigestPrincipal(principal session.PrincipalRef) (string, error) {
	if s == nil || s.cipher == nil {
		return "", ErrSecretCipherRequired
	}
	return s.cipher.DigestPrincipal(principal)
}

func (s *SecretStore) Save(connectionID string, value SecretValue) error {
	return s.SaveContext(context.Background(), connectionID, value)
}

func (s *SecretStore) SaveContext(ctx context.Context, connectionID string, value SecretValue) error {
	if err := validateConnectionID(connectionID); err != nil {
		return err
	}
	if _, err := NewSecretValue(value.raw); err != nil {
		return err
	}
	if s != nil && s.testValues != nil {
		s.testMu.Lock()
		s.testValues[connectionID] = value
		s.testMu.Unlock()
		return nil
	}
	if s == nil || s.pool == nil {
		return ErrSecretDatabasePoolRequired
	}
	return s.savePostgres(ctx, connectionID, value)
}

func (s *SecretStore) Load(connectionID string) (SecretValue, bool, error) {
	return s.LoadContext(context.Background(), connectionID)
}

func (s *SecretStore) LoadContext(ctx context.Context, connectionID string) (SecretValue, bool, error) {
	if err := validateConnectionID(connectionID); err != nil {
		return SecretValue{}, false, err
	}
	if s != nil && s.testValues != nil {
		s.testMu.RLock()
		value, ok := s.testValues[connectionID]
		s.testMu.RUnlock()
		return value, ok, nil
	}
	if s == nil || s.pool == nil {
		return SecretValue{}, false, ErrSecretDatabasePoolRequired
	}
	return s.loadPostgres(ctx, connectionID)
}

func (s *SecretStore) Delete(connectionID string) error {
	return s.DeleteContext(context.Background(), connectionID)
}

func (s *SecretStore) DeleteContext(ctx context.Context, connectionID string) error {
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
		return ErrSecretDatabasePoolRequired
	}
	return s.deletePostgres(ctx, connectionID)
}

func (s *SecretStore) savePostgres(ctx context.Context, name string, value SecretValue) error {
	if s.cipher == nil {
		return ErrSecretCipherRequired
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
  updated_at_ms = excluded.updated_at_ms`, namespace, name, SecretKeyVersion, nonce, ciphertext, aad, s.currentUnixMillis())
	clear(ciphertext)
	if err != nil {
		return fmt.Errorf("saving encrypted secret: %w", err)
	}
	return nil
}

func (s *SecretStore) loadPostgres(ctx context.Context, name string) (SecretValue, bool, error) {
	if s.cipher == nil {
		return SecretValue{}, false, ErrSecretCipherRequired
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
		return SecretValue{}, false, nil
	}
	if err != nil {
		return SecretValue{}, false, fmt.Errorf("loading encrypted secret: %w", err)
	}
	plaintext, err := s.cipher.Open(namespace, name, keyVersion, nonce, ciphertext, aad)
	clear(ciphertext)
	if err != nil {
		return SecretValue{}, false, err
	}
	raw := string(plaintext)
	clear(plaintext)
	value, err := NewSecretValue(raw)
	if err != nil {
		return SecretValue{}, false, errors.New("decrypted secret value is invalid")
	}
	return value, true, nil
}

func (s *SecretStore) deletePostgres(ctx context.Context, name string) error {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	if _, err := s.pool.Raw().Exec(queryCtx, "DELETE FROM secret_values WHERE namespace = $1 AND name = $2", secretNamespace(name), name); err != nil {
		return fmt.Errorf("deleting encrypted secret: %w", err)
	}
	return nil
}

func secretNamespace(name string) string {
	if strings.HasPrefix(name, "semantic_embedding.") {
		return "semantic_embedding"
	}
	return "model"
}

func (s *SecretStore) currentUnixMillis() int64 {
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

var _ json.Marshaler = SecretValue{}
