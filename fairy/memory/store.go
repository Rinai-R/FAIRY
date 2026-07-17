package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const (
	RelativePath = "intelligence/fairy.sqlite3"
	driverName   = "sqlite"
)

var (
	ErrRootRequired      = errors.New("config root is required")
	ErrDatabasePathEmpty = errors.New("memory database path is required")
	ErrDatabaseMissing   = errors.New("memory database does not exist")
)

type Summary struct {
	SchemaVersion           int64 `json:"schemaVersion"`
	Conversations           int64 `json:"conversations"`
	ActiveGlobalMemories    int64 `json:"activeGlobalMemories"`
	ActiveCharacterMemories int64 `json:"activeCharacterMemories"`
	NeedsReviewMemories     int64 `json:"needsReviewMemories"`
	PendingExtractionTurns  int64 `json:"pendingExtractionTurns"`
	RunningBatches          int64 `json:"runningBatches"`
	FailedBatches           int64 `json:"failedBatches"`
	CandidateKnowledge      int64 `json:"candidateKnowledge"`
	VerifiedKnowledge       int64 `json:"verifiedKnowledge"`
	ReadOnly                bool  `json:"readOnly"`
}

type Store struct {
	path string
}

func DatabasePath(root string) (string, error) {
	if root == "" {
		return "", ErrRootRequired
	}
	return filepath.Join(root, RelativePath), nil
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Summary() (Summary, error) {
	db, err := s.openReadOnly()
	if err != nil {
		return Summary{}, err
	}
	defer db.Close()

	schemaVersion, err := countScalar(db, "SELECT version FROM schema_meta WHERE singleton = 1")
	if err != nil {
		return Summary{}, fmt.Errorf("reading memory schema version: %w", err)
	}
	conversations, err := countScalar(db, "SELECT COUNT(*) FROM conversations")
	if err != nil {
		return Summary{}, fmt.Errorf("counting conversations: %w", err)
	}
	activeGlobalMemories, err := countScalar(db, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'global' AND review_status = 'ready' AND status = 'active'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting global memories: %w", err)
	}
	activeCharacterMemories, err := countScalar(db, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'character' AND review_status = 'ready' AND status = 'active'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting character memories: %w", err)
	}
	needsReviewMemories, err := countScalar(db, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'unassigned_legacy' AND review_status = 'needs_review' AND status = 'active'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting needs-review memories: %w", err)
	}
	pendingExtractionTurns, err := countScalar(db, "SELECT COUNT(*) FROM conversation_turns WHERE extraction_state = 'pending'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting pending extraction turns: %w", err)
	}
	runningBatches, err := countScalar(db, "SELECT COUNT(*) FROM extraction_batches WHERE status = 'running'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting running batches: %w", err)
	}
	failedBatches, err := countScalar(db, "SELECT COUNT(*) FROM extraction_batches WHERE status = 'failed'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting failed batches: %w", err)
	}
	candidateKnowledge, err := countScalar(db, "SELECT COUNT(*) FROM knowledge_entries WHERE status = 'candidate'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting candidate knowledge: %w", err)
	}
	verifiedKnowledge, err := countScalar(db, "SELECT COUNT(*) FROM knowledge_entries WHERE status = 'verified'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting verified knowledge: %w", err)
	}
	return Summary{
		SchemaVersion:           schemaVersion,
		Conversations:           conversations,
		ActiveGlobalMemories:    activeGlobalMemories,
		ActiveCharacterMemories: activeCharacterMemories,
		NeedsReviewMemories:     needsReviewMemories,
		PendingExtractionTurns:  pendingExtractionTurns,
		RunningBatches:          runningBatches,
		FailedBatches:           failedBatches,
		CandidateKnowledge:      candidateKnowledge,
		VerifiedKnowledge:       verifiedKnowledge,
		ReadOnly:                true,
	}, nil
}

func (s *Store) openReadOnly() (*sql.DB, error) {
	if s == nil || s.path == "" {
		return nil, ErrDatabasePathEmpty
	}
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrDatabaseMissing
		}
		return nil, fmt.Errorf("checking memory database: %w", err)
	}
	db, err := sql.Open(driverName, "file:"+s.path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("opening memory database read-only: %w", err)
	}
	if err := configureSQLiteConnection(db, true); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func configureSQLiteConnection(db *sql.DB, readOnly bool) error {
	if readOnly {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)
	} else {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	pragmas := []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	}
	if !readOnly {
		pragmas = append(pragmas,
			"PRAGMA journal_mode = WAL",
			"PRAGMA synchronous = NORMAL",
		)
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("configuring memory database connection: %w", err)
		}
	}
	return nil
}

func countScalar(db *sql.DB, query string) (int64, error) {
	var count int64
	if err := db.QueryRow(query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
