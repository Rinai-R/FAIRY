package importer

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func createIntelligenceFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "intelligence.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(legacySchemaV7); err != nil {
		t.Fatalf("create schema v7 fixture: %v", err)
	}
	return path
}

const legacySchemaV7 = `
PRAGMA foreign_keys = ON;
CREATE TABLE schema_meta(singleton INTEGER PRIMARY KEY CHECK(singleton = 1), version INTEGER NOT NULL);
INSERT INTO schema_meta(singleton, version) VALUES (1, 7);
CREATE TABLE conversations(id TEXT PRIMARY KEY, character_id TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE conversation_turns(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id), sequence INTEGER NOT NULL, status TEXT NOT NULL, error_code TEXT, error_message TEXT, error_retryable INTEGER, extraction_state TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, UNIQUE(conversation_id, sequence));
CREATE TABLE conversation_messages(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id), turn_id TEXT NOT NULL REFERENCES conversation_turns(id), sequence INTEGER NOT NULL, role TEXT NOT NULL, content TEXT NOT NULL, created_at_ms INTEGER NOT NULL, UNIQUE(conversation_id, sequence), UNIQUE(turn_id, role));
CREATE TABLE prompt_windows(conversation_id TEXT PRIMARY KEY REFERENCES conversations(id), revision INTEGER NOT NULL, summary TEXT, cutoff_message_sequence INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE turn_runtime_events(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id), turn_id TEXT NOT NULL REFERENCES conversation_turns(id), sequence INTEGER NOT NULL, event_type TEXT NOT NULL, state TEXT, code TEXT, metadata_json TEXT NOT NULL, created_at_ms INTEGER NOT NULL, UNIQUE(conversation_id, turn_id, sequence));
CREATE TABLE lane_continuations(conversation_id TEXT NOT NULL REFERENCES conversations(id), lane TEXT NOT NULL, previous_response_id TEXT NOT NULL, request_shape_hash TEXT NOT NULL, input_prefix_hash TEXT NOT NULL, response_item_hash TEXT NOT NULL, window_revision INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, PRIMARY KEY(conversation_id, lane));
CREATE TABLE context_windows(conversation_id TEXT NOT NULL REFERENCES conversations(id), lane TEXT NOT NULL, window_number INTEGER NOT NULL, first_window_id TEXT NOT NULL, previous_window_id TEXT, window_id TEXT NOT NULL, observed_prefill_tokens INTEGER, estimated_prefill_tokens INTEGER, last_trigger TEXT NOT NULL, failure_count INTEGER NOT NULL, prompt_window_revision INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, PRIMARY KEY(conversation_id, lane));
CREATE TABLE personal_memories(id TEXT PRIMARY KEY, kind TEXT NOT NULL, scope_kind TEXT NOT NULL, character_id TEXT, review_status TEXT NOT NULL, content TEXT NOT NULL, status TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL, source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, supersedes_id TEXT REFERENCES personal_memories(id), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE knowledge_entries(id TEXT PRIMARY KEY, topic TEXT NOT NULL, statement TEXT NOT NULL, status TEXT NOT NULL, verification_basis TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL, source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, supersedes_id TEXT REFERENCES knowledge_entries(id), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE knowledge_sources(knowledge_id TEXT NOT NULL REFERENCES knowledge_entries(id), source_id TEXT NOT NULL, title TEXT NOT NULL, url TEXT NOT NULL, snippet TEXT NOT NULL, rank INTEGER NOT NULL, fetched_at_ms INTEGER NOT NULL, PRIMARY KEY(knowledge_id, source_id));
CREATE TABLE extraction_batches(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id), character_id TEXT NOT NULL, status TEXT NOT NULL, first_turn_sequence INTEGER NOT NULL, last_turn_sequence INTEGER NOT NULL, error_code TEXT, error_message TEXT, error_retryable INTEGER, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE extraction_batch_turns(batch_id TEXT NOT NULL REFERENCES extraction_batches(id), turn_id TEXT NOT NULL REFERENCES conversation_turns(id), turn_sequence INTEGER NOT NULL, PRIMARY KEY(batch_id, turn_id), UNIQUE(batch_id, turn_sequence));
CREATE TABLE knowledge_ingest_jobs(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id), turn_id TEXT NOT NULL REFERENCES conversation_turns(id), query TEXT NOT NULL, title TEXT NOT NULL, url TEXT NOT NULL, snippet TEXT NOT NULL, rank INTEGER NOT NULL, fetched_at_ms INTEGER NOT NULL, status TEXT NOT NULL, error_message TEXT, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE memory_embedding_items(vector_rowid INTEGER PRIMARY KEY, item_kind TEXT NOT NULL, item_id TEXT NOT NULL, model_id TEXT NOT NULL, dimensions INTEGER NOT NULL, content_hash TEXT NOT NULL, status TEXT NOT NULL, error_code TEXT, error_message TEXT, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, UNIQUE(item_kind, item_id, model_id));
CREATE TABLE memory_embedding_jobs(id TEXT PRIMARY KEY, item_kind TEXT NOT NULL, item_id TEXT NOT NULL, model_id TEXT NOT NULL, dimensions INTEGER NOT NULL, content_hash TEXT NOT NULL, status TEXT NOT NULL, error_code TEXT, error_message TEXT, retryable INTEGER NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, UNIQUE(item_kind, item_id, model_id, content_hash));
CREATE VIRTUAL TABLE memory_embedding_vec USING vec0(embedding float[512]);
`
