//go:build sqlite_legacy

package memory

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDatabasePathRequiresRoot(t *testing.T) {
	_, err := DatabasePath("")
	if !errors.Is(err, ErrRootRequired) {
		t.Fatalf("DatabasePath() error = %v, want %v", err, ErrRootRequired)
	}
}

func TestNewStoreFromPoolRequiresPool(t *testing.T) {
	store, err := NewStoreFromPool(nil)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewStoreFromPool(nil) = (%v, %v), want (nil, %v)", store, err, ErrDatabasePoolEmpty)
	}
}

func TestNewMemoryServiceFromStoreRequiresStore(t *testing.T) {
	service, err := NewMemoryServiceFromStore(nil)
	if service != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewMemoryServiceFromStore(nil) = (%v, %v), want (nil, %v)", service, err, ErrDatabasePoolEmpty)
	}
}

func TestNewStoreFromPoolLeaseValidationRunsAfterPoolValidation(t *testing.T) {
	store, err := newStoreFromPoolWithLease(nil, "worker-1", time.Second)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("newStoreFromPoolWithLease(nil) = (%v, %v)", store, err)
	}
}

func TestDatabasePathUsesExistingRelativeLocation(t *testing.T) {
	got, err := DatabasePath("/tmp/fairy")
	if err != nil {
		t.Fatalf("DatabasePath() error = %v", err)
	}
	want := filepath.Join("/tmp/fairy", "intelligence", "fairy.sqlite3")
	if got != want {
		t.Fatalf("DatabasePath() = %q, want %q", got, want)
	}
}

func TestStoreSummaryRequiresExistingDatabase(t *testing.T) {
	_, err := NewStore(filepath.Join(t.TempDir(), "missing.sqlite3")).Summary()
	if !errors.Is(err, ErrDatabaseMissing) {
		t.Fatalf("Summary() error = %v, want %v", err, ErrDatabaseMissing)
	}
}

func TestStoreSummaryReadsExistingDatabaseWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fairy.sqlite3")
	db, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE schema_meta(singleton INTEGER PRIMARY KEY CHECK(singleton = 1), version INTEGER NOT NULL);
INSERT INTO schema_meta(singleton, version) VALUES (1, 3);
CREATE TABLE conversations(id TEXT PRIMARY KEY, character_id TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE conversation_turns(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, sequence INTEGER NOT NULL, status TEXT NOT NULL, extraction_state TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE personal_memories(id TEXT PRIMARY KEY, kind TEXT NOT NULL, scope_kind TEXT NOT NULL, character_id TEXT, review_status TEXT NOT NULL, content TEXT NOT NULL, status TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL, source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE knowledge_entries(id TEXT PRIMARY KEY, topic TEXT NOT NULL, statement TEXT NOT NULL, status TEXT NOT NULL, verification_basis TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL, source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE extraction_batches(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, character_id TEXT NOT NULL, status TEXT NOT NULL, first_turn_sequence INTEGER NOT NULL, last_turn_sequence INTEGER NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('conversation-1', 'character-1', 1, 1);
INSERT INTO conversation_turns(id, conversation_id, sequence, status, extraction_state, created_at_ms, updated_at_ms) VALUES ('turn-1', 'conversation-1', 1, 'completed', 'pending', 1, 1);
INSERT INTO personal_memories(id, kind, scope_kind, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms) VALUES ('memory-1', 'profile', 'global', 'ready', '喜欢安静', 'active', 9000, 'conversation-1', 'turn-1', 1, 1);
INSERT INTO personal_memories(id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms) VALUES ('memory-2', 'relationship', 'character', 'character-1', 'ready', '信任角色', 'active', 9000, 'conversation-1', 'turn-1', 1, 1);
INSERT INTO personal_memories(id, kind, scope_kind, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms) VALUES ('memory-3', 'relationship', 'unassigned_legacy', 'needs_review', '旧关系', 'active', 7000, 'conversation-1', 'turn-1', 1, 1);
INSERT INTO knowledge_entries(id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms) VALUES ('knowledge-1', '主题', '陈述', 'candidate', 'unverified', 8000, 'conversation-1', 'turn-1', 1, 1);
INSERT INTO knowledge_entries(id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms) VALUES ('knowledge-2', '主题2', '陈述2', 'verified', 'user_confirmed', 9000, 'conversation-1', 'turn-1', 1, 1);
INSERT INTO extraction_batches(id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence, created_at_ms, updated_at_ms) VALUES ('batch-1', 'conversation-1', 'character-1', 'running', 1, 1, 1, 1), ('batch-2', 'conversation-1', 'character-1', 'failed', 1, 1, 1, 1);
`)
	if err != nil {
		t.Fatalf("seed database error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	summary, err := NewStore(path).Summary()
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if summary.SchemaVersion != 3 || summary.Conversations != 1 || summary.ActiveGlobalMemories != 1 || summary.ActiveCharacterMemories != 1 || summary.NeedsReviewMemories != 1 || summary.PendingExtractionTurns != 1 || summary.RunningBatches != 1 || summary.FailedBatches != 1 || summary.CandidateKnowledge != 1 || summary.VerifiedKnowledge != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if !summary.ReadOnly {
		t.Fatal("ReadOnly = false, want true")
	}
}
