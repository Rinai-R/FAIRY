package secret

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// RelativePath matches the existing Rust/Tauri local secret database location
	// under the FAIRY harness/v1 config root.
	RelativePath = "model/secrets.sqlite3"

	driverName = "sqlite"
)

var (
	ErrRootRequired         = errors.New("config root is required")
	ErrStorePathRequired    = errors.New("secret store path is required")
	ErrConnectionIDRequired = errors.New("model connection_id is required")
	ErrInvalidConnectionID  = errors.New("model connection_id must not contain leading or trailing whitespace")
	ErrSecretRequired       = errors.New("model credential is required")
	ErrInvalidSecret        = errors.New("model credential must not contain leading or trailing whitespace")
)

// DatabasePath returns the existing FAIRY local model secret database path for a
// harness/v1 config root. It does not inspect, create, or migrate user data.
func DatabasePath(root string) (string, error) {
	if root == "" {
		return "", ErrRootRequired
	}
	return filepath.Join(root, RelativePath), nil
}

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

// Store is a minimal SQLite-backed model secret store compatible with the
// existing Rust/Tauri model_secrets table. It writes only the path supplied by
// its caller and never copies real user data into the repository.
type Store struct {
	path string
	now  func() time.Time
}

func NewStore(path string) *Store {
	return &Store{path: path, now: time.Now}
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) Save(connectionID string, value Value) error {
	if err := validateConnectionID(connectionID); err != nil {
		return err
	}
	if _, err := NewValue(value.raw); err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec(
		`INSERT INTO model_secrets(connection_id, secret, updated_at_ms)
		 VALUES (?1, ?2, ?3)
		 ON CONFLICT(connection_id) DO UPDATE SET
			secret = excluded.secret,
			updated_at_ms = excluded.updated_at_ms`,
		connectionID,
		value.raw,
		s.currentUnixMillis(),
	)
	if err != nil {
		return fmt.Errorf("saving model secret: %w", err)
	}
	return nil
}

func (s *Store) Load(connectionID string) (Value, bool, error) {
	if err := validateConnectionID(connectionID); err != nil {
		return Value{}, false, err
	}
	db, err := s.open()
	if err != nil {
		return Value{}, false, err
	}
	defer db.Close()

	var raw string
	err = db.QueryRow(
		"SELECT secret FROM model_secrets WHERE connection_id = ?1",
		connectionID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Value{}, false, nil
	}
	if err != nil {
		return Value{}, false, fmt.Errorf("loading model secret: %w", err)
	}
	value, err := NewValue(raw)
	if err != nil {
		return Value{}, false, err
	}
	return value, true, nil
}

func (s *Store) Delete(connectionID string) error {
	if err := validateConnectionID(connectionID); err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec("DELETE FROM model_secrets WHERE connection_id = ?1", connectionID); err != nil {
		return fmt.Errorf("deleting model secret: %w", err)
	}
	return nil
}

func (s *Store) open() (*sql.DB, error) {
	if s == nil || s.path == "" {
		return nil, ErrStorePathRequired
	}
	if parent := filepath.Dir(s.path); parent != "." {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, fmt.Errorf("creating model secret store directory: %w", err)
		}
	}
	db, err := sql.Open(driverName, s.path)
	if err != nil {
		return nil, fmt.Errorf("opening model secret store: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS model_secrets (
		connection_id TEXT PRIMARY KEY,
		secret TEXT NOT NULL,
		updated_at_ms INTEGER NOT NULL
	);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing model secret store: %w", err)
	}
	return db, nil
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
