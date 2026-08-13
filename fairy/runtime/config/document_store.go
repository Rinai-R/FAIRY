package config

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

var (
	ErrConfigDocumentStoreRequired = errors.New("config document SeekDB store is required")
	ErrConfigDocumentKeyInvalid    = errors.New("config document namespace or key is invalid")
	ErrConfigDocumentInvalid       = errors.New("config document must be a JSON object")
	ErrConfigRevisionConflict      = errors.New("config document revision conflict")
	ErrConfigQueryLimitInvalid     = errors.New("config document query limit must be greater than zero")
)

// ConfigDocument is one authoritative, versioned non-secret configuration
// record. Document is copied at the storage boundary so callers cannot mutate
// a value already accepted by the store.
type ConfigDocument struct {
	Namespace       string
	Key             string
	SchemaVersion   uint64
	Revision        uint64
	Document        json.RawMessage
	CreatedAtUnixMS int64
	UpdatedAtUnixMS int64
}

// DocumentStore persists non-secret configuration in SeekDB. Mutations use an
// explicit expected revision: zero creates an absent document, while a positive
// value replaces exactly that revision. A mismatch never writes partial state.
type DocumentStore struct {
	database   *sql.DB
	queryLimit time.Duration
	now        func() time.Time
}

func NewSeekDBDocumentStore(database *sql.DB, queryLimit time.Duration) (*DocumentStore, error) {
	if database == nil {
		return nil, ErrConfigDocumentStoreRequired
	}
	if queryLimit <= 0 {
		return nil, ErrConfigQueryLimitInvalid
	}
	return &DocumentStore{database: database, queryLimit: queryLimit, now: time.Now}, nil
}

func (s *DocumentStore) Get(ctx context.Context, namespace, key string) (ConfigDocument, bool, error) {
	if err := validateConfigDocumentKey(namespace, key); err != nil {
		return ConfigDocument{}, false, err
	}
	if s == nil || s.database == nil {
		return ConfigDocument{}, false, ErrConfigDocumentStoreRequired
	}
	queryCtx, cancel := s.queryContext(ctx)
	defer cancel()
	document, found, err := readConfigDocument(queryCtx, s.database, namespace, key, false)
	if err != nil {
		return ConfigDocument{}, false, fmt.Errorf("reading config document from SeekDB: %w", err)
	}
	return document, found, nil
}

func (s *DocumentStore) CompareAndSwap(ctx context.Context, namespace, key string, schemaVersion, expectedRevision uint64, raw json.RawMessage) (ConfigDocument, error) {
	if err := validateConfigDocumentKey(namespace, key); err != nil {
		return ConfigDocument{}, err
	}
	if schemaVersion == 0 || !isJSONObject(raw) {
		return ConfigDocument{}, ErrConfigDocumentInvalid
	}
	if s == nil || s.database == nil {
		return ConfigDocument{}, ErrConfigDocumentStoreRequired
	}
	queryCtx, cancel := s.queryContext(ctx)
	defer cancel()
	if err := canceledContextError(queryCtx); err != nil {
		return ConfigDocument{}, err
	}

	transaction, err := s.database.BeginTx(queryCtx, nil)
	if err != nil {
		return ConfigDocument{}, fmt.Errorf("starting config document transaction: %w", err)
	}
	defer transaction.Rollback()

	current, found, err := readConfigDocument(queryCtx, transaction, namespace, key, true)
	if err != nil {
		return ConfigDocument{}, fmt.Errorf("locking config document: %w", err)
	}
	if !found {
		if expectedRevision != 0 {
			return ConfigDocument{}, ErrConfigRevisionConflict
		}
		now := s.currentUnixMillis()
		if now < 0 {
			now = 0
		}
		_, err = transaction.ExecContext(queryCtx, `
INSERT INTO config_documents(
  namespace, document_key, schema_version, revision, document,
  created_at_ms, updated_at_ms
)
VALUES (?, ?, ?, 1, ?, ?, ?)`, namespace, key, schemaVersion, []byte(raw), now, now)
		if err != nil {
			if isSeekDBCreateConflict(err) {
				return ConfigDocument{}, ErrConfigRevisionConflict
			}
			return ConfigDocument{}, fmt.Errorf("creating config document: %w", err)
		}
		current = ConfigDocument{
			Namespace:       namespace,
			Key:             key,
			SchemaVersion:   schemaVersion,
			Revision:        1,
			Document:        bytes.Clone(raw),
			CreatedAtUnixMS: now,
			UpdatedAtUnixMS: now,
		}
	} else {
		if current.Revision != expectedRevision || current.Revision == math.MaxUint64 {
			return ConfigDocument{}, ErrConfigRevisionConflict
		}
		nextRevision := current.Revision + 1
		updatedAt := s.currentUnixMillis()
		if updatedAt < current.UpdatedAtUnixMS {
			updatedAt = current.UpdatedAtUnixMS
		}
		result, err := transaction.ExecContext(queryCtx, `
UPDATE config_documents
SET schema_version = ?, revision = ?, document = ?, updated_at_ms = ?
WHERE namespace = ? AND document_key = ? AND revision = ?`,
			schemaVersion, nextRevision, []byte(raw), updatedAt,
			namespace, key, expectedRevision,
		)
		if err != nil {
			return ConfigDocument{}, fmt.Errorf("replacing config document: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return ConfigDocument{}, fmt.Errorf("checking config document replacement: %w", err)
			}
			return ConfigDocument{}, ErrConfigRevisionConflict
		}
		current.SchemaVersion = schemaVersion
		current.Revision = nextRevision
		current.Document = bytes.Clone(raw)
		current.UpdatedAtUnixMS = updatedAt
	}
	if err := transaction.Commit(); err != nil {
		return ConfigDocument{}, fmt.Errorf("committing config document: %w", err)
	}
	return current, nil
}

func isSeekDBCreateConflict(err error) bool {
	var databaseError *gomysql.MySQLError
	if !errors.As(err, &databaseError) {
		return false
	}
	// 1062 is a duplicate unique key. SeekDB may instead choose one
	// concurrent gap-lock participant as the 1213 deadlock victim. For a
	// create-only expected revision both mean another writer won the CAS.
	return databaseError.Number == 1062 || databaseError.Number == 1213
}

type configDocumentQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readConfigDocument(ctx context.Context, querier configDocumentQuerier, namespace, key string, forUpdate bool) (ConfigDocument, bool, error) {
	statement := `
SELECT schema_version, revision, document, created_at_ms, updated_at_ms
FROM config_documents
WHERE namespace = ? AND document_key = ?`
	if forUpdate {
		statement += " FOR UPDATE"
	}
	document := ConfigDocument{Namespace: namespace, Key: key}
	var raw []byte
	err := querier.QueryRowContext(ctx, statement, namespace, key).Scan(
		&document.SchemaVersion,
		&document.Revision,
		&raw,
		&document.CreatedAtUnixMS,
		&document.UpdatedAtUnixMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigDocument{}, false, nil
	}
	if err != nil {
		return ConfigDocument{}, false, err
	}
	if document.SchemaVersion == 0 || document.Revision == 0 || !isJSONObject(raw) || document.UpdatedAtUnixMS < document.CreatedAtUnixMS {
		return ConfigDocument{}, false, ErrConfigDocumentInvalid
	}
	document.Document = bytes.Clone(raw)
	clear(raw)
	return document, true, nil
}

func (s *DocumentStore) queryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

func (s *DocumentStore) currentUnixMillis() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return now().UnixMilli()
}

func validateConfigDocumentKey(namespace, key string) error {
	if !portableConfigKey(namespace, 64) || !portableConfigKey(key, 128) {
		return ErrConfigDocumentKeyInvalid
	}
	return nil
}

func portableConfigKey(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("._:/-", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func isJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed)
}
