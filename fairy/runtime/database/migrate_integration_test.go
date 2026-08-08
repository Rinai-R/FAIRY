//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	gormschema "gorm.io/gorm/schema"
)

func TestMigrateAndVerifySchemaIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()

	if _, err := VerifySchema(ctx, pool); !errors.Is(err, ErrSchemaAbsent) {
		t.Fatalf("VerifySchema before migrate err = %v, want ErrSchemaAbsent", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if err != nil {
		t.Fatalf("VerifySchema after migrate error = %v", err)
	}
	if !status.Current || status.PresentObjects != status.ExpectedObjects {
		t.Fatalf("status = %#v", status)
	}
	for _, table := range schemaTableNames() {
		assertRegclass(t, ctx, pool, table, true)
	}
	for _, model := range schemaModels() {
		parsed, err := gormschema.Parse(model, &sync.Map{}, gormschema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse schema model %T: %v", model, err)
		}
		for name := range parsed.ParseCheckConstraints() {
			assertConstraint(t, ctx, pool, parsed.Table, name, true)
		}
		for _, index := range parsed.ParseIndexes() {
			assertRegclass(t, ctx, pool, index.Name, true)
		}
	}
	for _, constraint := range postgresConstraints {
		assertConstraint(t, ctx, pool, constraint.Table, constraint.Name, true)
	}
	for _, index := range postgresIndexes {
		assertRegclass(t, ctx, pool, index.Name, true)
	}
	assertRegclass(t, ctx, pool, "fairy_schema_migrations", false)
	assertRegclass(t, ctx, pool, "sqlite_import_runs", false)
	for _, table := range []string{
		"feedback_events",
		"knowledge_documents",
		"personal_memory_evidence",
		"social_reply_feedback",
		"social_person_notes",
		"knowledge_sources",
		"knowledge_document_versions",
		"knowledge_chunks",
		"knowledge_evidence",
		"extraction_batches",
		"extraction_batch_turns",
		"knowledge_ingest_jobs",
	} {
		assertRegclass(t, ctx, pool, table, false)
	}
	for _, column := range [][2]string{
		{"knowledge_entries", "confidence_basis_points"},
		{"conversation_turns", "extraction_attempt_count"},
		{"secret_values", "key_version"},
		{"social_memory_entries", "feedback_score_basis_points"},
		{"social_memory_feedback_events", "observed_message_count"},
	} {
		assertColumnType(t, ctx, pool, column[0], column[1], "integer")
	}
	assertColumnType(t, ctx, pool, "personal_memories", "evidence_ids_json", "jsonb")
	assertColumnType(t, ctx, pool, "knowledge_entries", "source_url", "text")
	assertColumnType(t, ctx, pool, "knowledge_entries", "source_content_hash", "text")
	assertColumnType(t, ctx, pool, "knowledge_entries", "evidence_text", "text")
	assertColumnType(t, ctx, pool, "social_memory_feedback_events", "evidence_message_ids", "jsonb")
	var forbiddenFeedbackColumns int
	if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'social_memory_feedback_events'
  AND column_name IN ('reply', 'reply_text', 'observation', 'observation_text', 'prompt', 'reason', 'body', 'content')`).Scan(&forbiddenFeedbackColumns); err != nil {
		t.Fatalf("checking social feedback privacy columns: %v", err)
	}
	if forbiddenFeedbackColumns != 0 {
		t.Fatalf("social feedback event has %d forbidden body columns", forbiddenFeedbackColumns)
	}

	var hasTrgm bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')").Scan(&hasTrgm); err != nil {
		t.Fatalf("checking pg_trgm: %v", err)
	}
	if !hasTrgm {
		t.Fatal("pg_trgm extension is not installed")
	}
	var hasVector bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'vector')").Scan(&hasVector); err != nil {
		t.Fatalf("checking pgvector: %v", err)
	}
	if !hasVector {
		t.Fatal("vector extension is not installed")
	}
	for _, table := range []string{"personal_memories", "knowledge_entries"} {
		assertVectorColumnType(t, ctx, pool, table, "embedding", "public.vector(512)")
		assertVectorColumnType(t, ctx, pool, table, "embedding_v2", "public.vector(1024)")
	}
}

func TestMigrateAddsBGEV2WithoutChangingV1VectorsIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	memoryV1 := pgvector.NewVector(make([]float32, 512))
	memoryV1.Slice()[0] = 0.25
	knowledgeV1 := pgvector.NewVector(make([]float32, 512))
	knowledgeV1.Slice()[1] = 0.75
	if _, err := pool.Exec(ctx, `INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('embedding-v2-conversation', 'character', 1, 1)`); err != nil {
		t.Fatalf("seed v1 conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('embedding-v2-turn', 'embedding-v2-conversation', 1, 'completed', 'user', 'processed', 1, 1)`); err != nil {
		t.Fatalf("seed v1 turn: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO personal_memories(
  id, kind, scope_kind, review_status, content, status, confidence_basis_points,
  source_conversation_id, source_turn_id, evidence_ids_json,
  embedding_model_id, embedding_content_hash, embedding, created_at_ms, updated_at_ms
) VALUES (
  'embedding-v2-memory', 'preference', 'global', 'ready', 'v1 memory', 'active', 9000,
  'embedding-v2-conversation', 'embedding-v2-turn', '[]'::jsonb,
  'legacy-model', repeat('a', 64), $1::public.vector, 1, 1
)`, memoryV1.String()); err != nil {
		t.Fatalf("seed v1 personal vector: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id,
  embedding_model_id, embedding_content_hash, embedding, created_at_ms, updated_at_ms
) VALUES (
  'embedding-v2-knowledge', 'topic', 'v1 knowledge', 'verified', 'direct', 9000,
  'embedding-v2-conversation', 'embedding-v2-turn',
  'legacy-model', repeat('b', 64), $1::public.vector, 1, 1
)`, knowledgeV1.String()); err != nil {
		t.Fatalf("seed v1 knowledge vector: %v", err)
	}
	if _, err := pool.Exec(ctx, `
ALTER TABLE personal_memories DROP CONSTRAINT personal_memories_embedding_v2_check;
ALTER TABLE knowledge_entries DROP CONSTRAINT knowledge_entries_embedding_v2_check;
ALTER TABLE personal_memories
  DROP COLUMN embedding_model_id_v2,
  DROP COLUMN embedding_content_hash_v2,
  DROP COLUMN embedding_v2;
ALTER TABLE knowledge_entries
  DROP COLUMN embedding_model_id_v2,
  DROP COLUMN embedding_content_hash_v2,
  DROP COLUMN embedding_v2;
UPDATE fairy_schema_state SET revision = '2026-07-31-public-social-feedback-1' WHERE id = 1`); err != nil {
		t.Fatalf("prepare v1-only schema: %v", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate(v1-only) error = %v", err)
	}
	for _, test := range []struct {
		table string
		id    string
		v1    string
	}{
		{table: "personal_memories", id: "embedding-v2-memory", v1: memoryV1.String()},
		{table: "knowledge_entries", id: "embedding-v2-knowledge", v1: knowledgeV1.String()},
	} {
		var gotV1 string
		var v2Empty bool
		query := fmt.Sprintf("SELECT embedding::text, embedding_model_id_v2 IS NULL AND embedding_content_hash_v2 IS NULL AND embedding_v2 IS NULL FROM %s WHERE id = $1", pgx.Identifier{test.table}.Sanitize())
		if err := pool.QueryRow(ctx, query, test.id).Scan(&gotV1, &v2Empty); err != nil {
			t.Fatalf("read upgraded %s: %v", test.table, err)
		}
		if gotV1 != test.v1 || !v2Empty {
			t.Fatalf("upgraded %s v1 preserved = %v, v2 empty = %v", test.table, gotV1 == test.v1, v2Empty)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE personal_memories SET embedding_model_id_v2 = 'BAAI/bge-m3' WHERE id = 'embedding-v2-memory'`); err == nil {
		t.Fatal("partial v2 metadata must be rejected")
	}
	if _, err := pool.Exec(ctx, `UPDATE personal_memories SET embedding_v2 = '[1,2,3]'::public.vector WHERE id = 'embedding-v2-memory'`); err == nil {
		t.Fatal("wrong v2 vector dimensions must be rejected")
	}
}

func TestMigrateBackfillsSuppressedSocialMemoryWithoutInventingEventsIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	const updatedAt = int64(123456)
	if _, err := pool.Exec(ctx, `INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('social-migration-conversation', 'character-1', 1, 1)`); err != nil {
		t.Fatalf("prepare previous social feedback conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO social_memory_entries(
  id, character_id, conversation_id, kind, situation, content, recall_cue,
  content_hash, status, source_start_ms, source_end_ms, use_count,
  positive_count, negative_count, unknown_count, created_at_ms, updated_at_ms
) VALUES (
  'social-migration-entry', 'character-1', 'social-migration-conversation', 'behavior',
  '测试情境', '测试内容', '测试线索', repeat('a', 64), 'active', 1, 2, 3, 0, 3, 0, 1, $1
)
`, updatedAt); err != nil {
		t.Fatalf("prepare previous social feedback row: %v", err)
	}
	if _, err := pool.Exec(ctx, `
ALTER TABLE social_memory_entries DROP CONSTRAINT social_memory_entries_invariants_check;
UPDATE social_memory_entries SET status = 'suppressed' WHERE id = 'social-migration-entry';
DROP TABLE social_memory_feedback_events;
ALTER TABLE social_memory_entries
  DROP COLUMN feedback_evaluation_count,
  DROP COLUMN feedback_adopted_count,
  DROP COLUMN feedback_positive_count,
  DROP COLUMN feedback_partial_count,
  DROP COLUMN feedback_negative_count,
  DROP COLUMN feedback_score_basis_points,
  DROP COLUMN feedback_quarantined_until_ms;
UPDATE fairy_schema_state SET revision = '2026-07-30-context-retention-1' WHERE id = 1`); err != nil {
		t.Fatalf("prepare previous social feedback schema: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate(previous social feedback schema) error = %v", err)
	}
	var status string
	var quarantine int64
	var score int
	if err := pool.QueryRow(ctx, `
SELECT status, feedback_quarantined_until_ms, feedback_score_basis_points
FROM social_memory_entries WHERE id = 'social-migration-entry'`).Scan(&status, &quarantine, &score); err != nil {
		t.Fatalf("read migrated social entry: %v", err)
	}
	if status != "suppressed" || quarantine != updatedAt+604800000 || score != 0 {
		t.Fatalf("migrated social entry = status:%s quarantine:%d score:%d", status, quarantine, score)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM social_memory_feedback_events").Scan(&eventCount); err != nil {
		t.Fatalf("count migrated feedback events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("migration invented %d social feedback events", eventCount)
	}
}

func TestMigrateIsIdempotentIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('sentinel', 'character', 1, 1)"); err != nil {
		t.Fatalf("insert sentinel: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM conversations WHERE id = 'sentinel'").Scan(&count); err != nil {
		t.Fatalf("count sentinel: %v", err)
	}
	if count != 1 {
		t.Fatalf("sentinel count = %d, want 1", count)
	}
}

func TestMigrateProjectsAndDropsLegacyMemoryKnowledgeSchemaIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	db, closeDB, err := openGORM(pool)
	if err != nil {
		t.Fatalf("open GORM: %v", err)
	}
	defer closeDB()
	if _, err := pool.Exec(ctx, `
CREATE TABLE knowledge_documents (
  id text PRIMARY KEY,
  canonical_url text NOT NULL UNIQUE,
  title text NOT NULL DEFAULT '',
  content text NOT NULL DEFAULT '',
  content_hash text NOT NULL DEFAULT '',
  content_type text NOT NULL DEFAULT '',
  fetched_at_ms bigint NOT NULL DEFAULT 0,
  etag text NOT NULL DEFAULT '',
  last_modified text NOT NULL DEFAULT '',
  reconciler_revision text NOT NULL DEFAULT '',
  created_at_ms bigint NOT NULL,
  updated_at_ms bigint NOT NULL
);
CREATE TABLE feedback_events (
  id text PRIMARY KEY,
  type text NOT NULL,
  conversation_id text NOT NULL,
  turn_id text NOT NULL,
  character_id text NOT NULL,
  payload_json jsonb NOT NULL,
  status text NOT NULL,
  lease_owner text,
  lease_expires_at_ms bigint,
  claim_group_id text,
  attempt_count integer NOT NULL DEFAULT 0,
  next_attempt_at_ms bigint NOT NULL DEFAULT 0,
  error_category text,
  error_message text,
  created_at_ms bigint NOT NULL,
  updated_at_ms bigint NOT NULL
)`); err != nil {
		t.Fatalf("create previous intermediate tables: %v", err)
	}
	if err := db.WithContext(ctx).AutoMigrate(
		&personalMemoryEvidenceSchema{},
		&knowledgeSourceSchema{},
		&knowledgeDocumentVersionSchema{},
		&knowledgeChunkSchema{},
		&knowledgeEvidenceSchema{},
		&extractionBatchSchema{},
		&extractionBatchTurnSchema{},
		&knowledgeIngestJobSchema{},
		&socialReplyFeedbackSchema{},
		&socialPersonNoteSchema{},
	); err != nil {
		t.Fatalf("create legacy auxiliary tables: %v", err)
	}
	if _, err := pool.Exec(ctx, `
ALTER TABLE knowledge_documents
  ADD COLUMN current_version_id text,
  ADD COLUMN current_content_hash text;
ALTER TABLE knowledge_entries
  ADD COLUMN document_id text,
  ADD COLUMN subject text,
  ADD COLUMN predicate text,
  ADD COLUMN value text,
  ADD COLUMN fact_key text;
UPDATE fairy_schema_state SET revision = '2026-07-29-knowledge-ingest-task-columns-2' WHERE id = 1;

INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('legacy-conversation', 'legacy-character', 1, 1);
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('legacy-turn', 'legacy-conversation', 1, 'completed', 'user', 'pending', 1, 1);
INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms)
VALUES ('legacy-turn', 'observation-1', 1);
INSERT INTO personal_memories(
  id, kind, scope_kind, review_status, content, status, confidence_basis_points,
  source_conversation_id, source_turn_id, created_at_ms, updated_at_ms
) VALUES (
  'legacy-memory', 'preference', 'global', 'ready', '喜欢直接回答', 'active', 9000,
  'legacy-conversation', 'legacy-turn', 1, 1
);
INSERT INTO personal_memory_evidence(memory_id, turn_id, evidence_id, created_at_ms)
VALUES ('legacy-memory', 'legacy-turn', 'observation-1', 1);
INSERT INTO social_person_notes(
  id, character_id, conversation_id, sender_id, sender_name, note, created_at_ms, updated_at_ms
) VALUES (
  'legacy-note', 'legacy-character', 'legacy-conversation', 'sender-1', '群友一', '偏好短回复', 1, 2
);
INSERT INTO knowledge_documents(
  id, canonical_url, title, current_version_id, current_content_hash, created_at_ms, updated_at_ms
) VALUES (
  'legacy-document', 'https://legacy.example/document', 'Legacy', 'legacy-version',
  repeat('a', 64), 1, 2
);
INSERT INTO knowledge_document_versions(
  id, document_id, content_hash, content_type, status, fetched_at_ms,
  etag, last_modified, reconciler_revision, created_at_ms
) VALUES (
  'legacy-version', 'legacy-document', repeat('a', 64), 'text/plain', 'current', 2,
  '"etag"', 'yesterday', repeat('b', 64), 2
);
INSERT INTO knowledge_chunks(id, version_id, ordinal, text, text_hash, created_at_ms)
VALUES ('legacy-chunk', 'legacy-version', 0, '完整旧正文', repeat('c', 64), 2);
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id, created_at_ms, updated_at_ms
) VALUES (
  'legacy-knowledge', '主题', '公开知识正文', 'verified', 'web', 9000,
  'legacy-conversation', 'legacy-turn', 2, 2
);
INSERT INTO knowledge_evidence(knowledge_id, chunk_id, version_id, active, created_at_ms)
VALUES ('legacy-knowledge', 'legacy-chunk', 'legacy-version', true, 2);
INSERT INTO extraction_batches(
  id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence,
  attempt_count, created_at_ms, updated_at_ms
) VALUES (
  'legacy-batch', 'legacy-conversation', 'legacy-character', 'pending', 1, 1, 0, 2, 2
);
INSERT INTO extraction_batch_turns(batch_id, turn_id, turn_sequence)
VALUES ('legacy-batch', 'legacy-turn', 1);
INSERT INTO knowledge_ingest_jobs(
  id, conversation_id, turn_id, task_id, source_json, status, created_at_ms, updated_at_ms
) VALUES (
  'legacy-job', 'legacy-conversation', 'legacy-turn', 'legacy-task',
  '{"id":"source-1","title":"Legacy","url":"https://legacy.example/document","snippet":"","rank":1,"fetchedAtUnixMs":2}'::jsonb,
  'pending', 2, 2
)
`); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate(legacy schema) error = %v", err)
	}
	for _, table := range []string{
		"feedback_events", "knowledge_documents",
		"personal_memory_evidence", "social_reply_feedback", "social_person_notes",
		"knowledge_sources", "knowledge_document_versions", "knowledge_chunks",
		"knowledge_evidence", "extraction_batches", "extraction_batch_turns",
		"knowledge_ingest_jobs",
	} {
		assertRegclass(t, ctx, pool, table, false)
	}
	var evidenceIDs string
	if err := pool.QueryRow(ctx, "SELECT evidence_ids_json::text FROM personal_memories WHERE id = 'legacy-memory'").Scan(&evidenceIDs); err != nil {
		t.Fatalf("read projected personal evidence: %v", err)
	}
	if evidenceIDs != `["observation-1"]` {
		t.Fatalf("personal evidence = %s", evidenceIDs)
	}
	var note string
	if err := pool.QueryRow(ctx, "SELECT content FROM social_memory_entries WHERE kind = 'person_note' AND sender_id = 'sender-1'").Scan(&note); err != nil {
		t.Fatalf("read projected social note: %v", err)
	}
	if note != "偏好短回复" {
		t.Fatalf("social note = %q", note)
	}
	var sourceURL, sourceHash, revision, evidence string
	if err := pool.QueryRow(ctx, `
SELECT source_url, source_content_hash, reconciler_revision, evidence_text
FROM knowledge_entries WHERE id = 'legacy-knowledge'
`).Scan(&sourceURL, &sourceHash, &revision, &evidence); err != nil {
		t.Fatalf("read projected knowledge: %v", err)
	}
	if sourceURL != "https://legacy.example/document" ||
		sourceHash != strings.Repeat("a", 64) ||
		revision != strings.Repeat("b", 64) ||
		evidence != "完整旧正文" {
		t.Fatalf("projected knowledge = (%q, %q, %q, %q)", sourceURL, sourceHash, revision, evidence)
	}
	var extractionState string
	var extractionAttempts int
	if err := pool.QueryRow(ctx, `
SELECT extraction_state, extraction_attempt_count
FROM conversation_turns
WHERE id = 'legacy-turn'
`).Scan(&extractionState, &extractionAttempts); err != nil {
		t.Fatalf("read projected extraction state: %v", err)
	}
	if extractionState != "pending" || extractionAttempts != 0 {
		t.Fatalf("projected extraction = state:%q attempts:%d", extractionState, extractionAttempts)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("repeat Migrate() error = %v", err)
	}
}

func TestMigrateCleansIndependentlyRemainingCognitiveIntermediatesIntegration(t *testing.T) {
	t.Run("knowledge document without feedback table", func(t *testing.T) {
		ctx := t.Context()
		pool := openIsolatedPool(t, ctx)
		defer pool.Close()
		if err := Migrate(ctx, pool); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
		if _, err := pool.Exec(ctx, `
CREATE TABLE knowledge_documents (
  id text PRIMARY KEY,
  canonical_url text NOT NULL UNIQUE,
  title text NOT NULL DEFAULT '',
  content text NOT NULL DEFAULT '',
  content_hash text NOT NULL DEFAULT '',
  content_type text NOT NULL DEFAULT '',
  fetched_at_ms bigint NOT NULL DEFAULT 0,
  etag text NOT NULL DEFAULT '',
  last_modified text NOT NULL DEFAULT '',
  reconciler_revision text NOT NULL DEFAULT '',
  created_at_ms bigint NOT NULL,
  updated_at_ms bigint NOT NULL
);
ALTER TABLE knowledge_entries ADD COLUMN document_id text;
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('partial-document-conversation', 'character', 1, 1);
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('partial-document-turn', 'partial-document-conversation', 1, 'completed', 'user', 'processed', 1, 1);
INSERT INTO knowledge_documents(
  id, canonical_url, title, content, content_hash, content_type,
  fetched_at_ms, etag, last_modified, reconciler_revision, created_at_ms, updated_at_ms
) VALUES (
  'partial-document', 'https://partial.example/document', 'Partial', '正文',
  repeat('a', 64), 'text/plain', 2, '"etag"', 'today', repeat('b', 64), 1, 2
);
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id, document_id, evidence_text, created_at_ms, updated_at_ms
) VALUES (
  'partial-document-knowledge', '主题', '知识', 'verified', 'web', 9000,
  'partial-document-conversation', 'partial-document-turn', 'partial-document', '正文', 2, 2
)`); err != nil {
			t.Fatalf("prepare independently remaining document table: %v", err)
		}
		if err := Migrate(ctx, pool); err != nil {
			t.Fatalf("Migrate(partial document) error = %v", err)
		}
		assertRegclass(t, ctx, pool, "knowledge_documents", false)
		assertColumnAbsent(t, ctx, pool, "knowledge_entries", "document_id")
		var sourceURL string
		if err := pool.QueryRow(ctx, "SELECT source_url FROM knowledge_entries WHERE id = 'partial-document-knowledge'").Scan(&sourceURL); err != nil {
			t.Fatalf("read projected source URL: %v", err)
		}
		if sourceURL != "https://partial.example/document" {
			t.Fatalf("projected source URL = %q", sourceURL)
		}
	})

	t.Run("feedback table without knowledge document", func(t *testing.T) {
		ctx := t.Context()
		pool := openIsolatedPool(t, ctx)
		defer pool.Close()
		if err := Migrate(ctx, pool); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
		if _, err := pool.Exec(ctx, `
CREATE TABLE feedback_events (
  id text PRIMARY KEY,
  type text NOT NULL,
  conversation_id text NOT NULL,
  turn_id text NOT NULL,
  character_id text NOT NULL,
  payload_json jsonb NOT NULL,
  status text NOT NULL,
  lease_owner text,
  lease_expires_at_ms bigint,
  claim_group_id text,
  attempt_count integer NOT NULL DEFAULT 0,
  next_attempt_at_ms bigint NOT NULL DEFAULT 0,
  error_category text,
  error_message text,
  created_at_ms bigint NOT NULL,
  updated_at_ms bigint NOT NULL
);
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('partial-feedback-conversation', 'character', 1, 1);
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('partial-feedback-turn', 'partial-feedback-conversation', 1, 'completed', 'user', 'processed', 1, 1);
INSERT INTO feedback_events(
  id, type, conversation_id, turn_id, character_id, payload_json, status,
  attempt_count, next_attempt_at_ms, created_at_ms, updated_at_ms
) VALUES (
  'partial-feedback', 'personal_memory', 'partial-feedback-conversation',
  'partial-feedback-turn', 'character', '{}'::jsonb, 'pending', 1, 7, 1, 2
)`); err != nil {
			t.Fatalf("prepare independently remaining feedback table: %v", err)
		}
		if err := Migrate(ctx, pool); err != nil {
			t.Fatalf("Migrate(partial feedback) error = %v", err)
		}
		assertRegclass(t, ctx, pool, "feedback_events", false)
		var state string
		var attempts int
		var nextAttempt int64
		if err := pool.QueryRow(ctx, `
SELECT extraction_state, extraction_attempt_count, extraction_next_attempt_at_ms
FROM conversation_turns
WHERE id = 'partial-feedback-turn'`).Scan(&state, &attempts, &nextAttempt); err != nil {
			t.Fatalf("read recovered extraction state: %v", err)
		}
		if state != "pending" || attempts != 1 || nextAttempt != 7 {
			t.Fatalf("recovered extraction = state:%q attempts:%d next:%d", state, attempts, nextAttempt)
		}
	})
}

func TestMigrateSerializesConcurrentCallersIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Migrate(ctx, pool)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Migrate() error = %v", err)
		}
	}
	status, err := VerifySchema(ctx, pool)
	if err != nil || !status.Current {
		t.Fatalf("VerifySchema() status = %#v, err = %v", status, err)
	}
}

func TestMigratePreservesDomainConstraintsIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	_, err := pool.Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('invalid', 'character', -1, -1)")
	if err == nil {
		t.Fatal("negative conversation timestamps must be rejected")
	}
	_, err = pool.Exec(ctx, "INSERT INTO secret_values(namespace, name, key_version, nonce, ciphertext, aad, created_at_ms, updated_at_ms) VALUES ('model', 'key', 1, $1, $2, 'model:key', 1, 1)", []byte("short"), []byte("ciphertext"))
	if err == nil {
		t.Fatal("invalid secret nonce length must be rejected")
	}
	if _, err := pool.Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('constraint-conversation', 'character', 1, 1)"); err != nil {
		t.Fatalf("insert constraint conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('invalid-status-turn', 'constraint-conversation', 1, 'unknown', 'user', 'pending', 1, 1)
`); err == nil {
		t.Fatal("invalid turn status must be rejected")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('constraint-turn', 'constraint-conversation', 1, 'completed', 'user', 'pending', 1, 1)
`); err != nil {
		t.Fatalf("insert constraint turn: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms)
VALUES ('invalid-parts-message', 'constraint-conversation', 'constraint-turn', 1, 'assistant', 'text', '{}'::jsonb, 1)
`); err == nil {
		t.Fatal("non-array expression parts must be rejected")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO tool_executions(id, conversation_id, turn_id, call_id, tool_name, status, deadline_at_ms, created_at_ms, updated_at_ms)
VALUES ('invalid-completed-tool', 'constraint-conversation', 'constraint-turn', 'call', 'desktop_observe', 'completed', 2, 1, 1)
`); err == nil {
		t.Fatal("completed tool execution without result fields must be rejected")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO stickers(id, content_sha256, mime_type, byte_count, content, description, tags, status, created_at_ms, updated_at_ms)
VALUES ('sticker-1', repeat('a', 64), 'image/png', 1, '\x01'::bytea, '', '[]'::jsonb, 'draft', 1, 1)
`); err != nil {
		t.Fatalf("insert first sticker hash: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO stickers(id, content_sha256, mime_type, byte_count, content, description, tags, status, created_at_ms, updated_at_ms)
VALUES ('sticker-2', repeat('a', 64), 'image/png', 1, '\x02'::bytea, '', '[]'::jsonb, 'draft', 1, 1)
`); err == nil {
		t.Fatal("duplicate sticker hash must be rejected")
	}
}

func TestVerifySchemaReportsPartialObjectsWithoutMutatingIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if _, err := pool.Exec(ctx, "CREATE TABLE conversations (id text PRIMARY KEY)"); err != nil {
		t.Fatalf("create partial table: %v", err)
	}
	fingerprintBefore := catalogFingerprint(t, ctx, pool)
	status, err := VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaAbsent) {
		t.Fatalf("VerifySchema() err = %v, want ErrSchemaAbsent", err)
	}
	if status.Current || len(status.MissingObjects) == 0 || status.MissingObjects[0] != "table:fairy_schema_state" {
		t.Fatalf("partial status = %#v", status)
	}
	if fingerprintAfter := catalogFingerprint(t, ctx, pool); fingerprintAfter != fingerprintBefore {
		t.Fatalf("VerifySchema mutated partial catalog: before %s, after %s", fingerprintBefore, fingerprintAfter)
	}
	assertRegclass(t, ctx, pool, "conversation_turns", false)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate(partial schema) error = %v", err)
	}
	status, err = VerifySchema(ctx, pool)
	if err != nil || !status.Current {
		t.Fatalf("VerifySchema() after additive migrate status = %#v, err = %v", status, err)
	}
}

func TestMigrateUpgradesPreviousStickerSchemaAndPreservesRowsIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('upgrade-conversation', 'character', 1, 1);
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
VALUES ('upgrade-turn', 'upgrade-conversation', 1, 'completed', 'user', 'pending', 1, 1);
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms)
VALUES ('upgrade-message', 'upgrade-conversation', 'upgrade-turn', 1, 'assistant', 'sentinel', '[]'::jsonb, 1);
DELETE FROM fairy_schema_state WHERE id = 1;
ALTER TABLE conversations ADD CONSTRAINT previous_conversations_character_check CHECK (character_id <> '');
ALTER TABLE conversation_messages DROP CONSTRAINT conversation_messages_invariants_check;
ALTER TABLE conversation_messages DROP COLUMN expression_parts;
DROP TABLE stickers;
`); err != nil {
		t.Fatalf("prepare previous sticker schema: %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifySchema() err = %v, want ErrSchemaNotCurrent", err)
	}
	if status.Current {
		t.Fatalf("previous schema unexpectedly current: %#v", status)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate(previous schema) error = %v", err)
	}
	status, err = VerifySchema(ctx, pool)
	if err != nil || !status.Current {
		t.Fatalf("VerifySchema() after upgrade status = %#v, err = %v", status, err)
	}
	var content string
	var expressionParts string
	if err := pool.QueryRow(ctx, `
SELECT content, expression_parts::text
FROM conversation_messages
WHERE id = 'upgrade-message'
`).Scan(&content, &expressionParts); err != nil {
		t.Fatalf("read preserved message: %v", err)
	}
	if content != "sentinel" || expressionParts != "[]" {
		t.Fatalf("preserved message content = %q, expression_parts = %q", content, expressionParts)
	}
	assertRegclass(t, ctx, pool, "stickers", true)
	assertConstraint(t, ctx, pool, "conversations", "previous_conversations_character_check", true)
}

func TestMigrateLegacyProjectionFailureRollsBackIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	db, closeDB, err := openGORM(pool)
	if err != nil {
		t.Fatalf("open GORM: %v", err)
	}
	defer closeDB()
	if err := db.WithContext(ctx).AutoMigrate(&knowledgeDocumentVersionSchema{}); err != nil {
		t.Fatalf("create partial legacy schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE fairy_schema_state
SET revision = '2026-07-29-knowledge-ingest-task-columns-2'
WHERE id = 1
`); err != nil {
		t.Fatalf("prepare previous revision: %v", err)
	}

	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("Migrate(partial legacy schema) error = nil")
	}
	assertRegclass(t, ctx, pool, "knowledge_document_versions", true)
	var revision string
	if err := pool.QueryRow(ctx, "SELECT revision FROM fairy_schema_state WHERE id = 1").Scan(&revision); err != nil {
		t.Fatalf("read revision after rollback: %v", err)
	}
	if revision != "2026-07-29-knowledge-ingest-task-columns-2" {
		t.Fatalf("revision after rollback = %q", revision)
	}
}

func TestMigrateAndVerifyAllowUnrelatedTablesIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE operator_notes (id integer PRIMARY KEY, note text NOT NULL)"); err != nil {
		t.Fatalf("create unrelated table: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO operator_notes(id, note) VALUES (1, 'keep me')"); err != nil {
		t.Fatalf("insert unrelated row: %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if err != nil || !status.Current {
		t.Fatalf("VerifySchema() with unrelated table status = %#v, err = %v", status, err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() with unrelated table error = %v", err)
	}
	var note string
	if err := pool.QueryRow(ctx, "SELECT note FROM operator_notes WHERE id = 1").Scan(&note); err != nil {
		t.Fatalf("read unrelated row: %v", err)
	}
	if note != "keep me" {
		t.Fatalf("unrelated row = %q, want keep me", note)
	}
}

func TestMigrateFailsWhenExistingRowsViolateCurrentConstraintIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
DELETE FROM fairy_schema_state WHERE id = 1;
ALTER TABLE conversations DROP CONSTRAINT conversations_invariants_check;
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('invalid-existing-row', 'character', -1, -1);
`); err != nil {
		t.Fatalf("prepare invalid existing row: %v", err)
	}
	err := Migrate(ctx, pool)
	if err == nil {
		t.Fatal("Migrate() with invalid existing row succeeded")
	}
	if !strings.Contains(err.Error(), "conversations_invariants_check") {
		t.Fatalf("Migrate() error = %v, want constraint name", err)
	}
	assertConstraint(t, ctx, pool, "conversations", "conversations_invariants_check", false)
	status, verifyErr := VerifySchema(ctx, pool)
	if !errors.Is(verifyErr, ErrSchemaNotCurrent) || status.Current {
		t.Fatalf("VerifySchema() after failed migration status = %#v, err = %v", status, verifyErr)
	}
}

func TestVerifySchemaRejectsMissingAndOldRevisionIntegration(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedPool(t, ctx)
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE fairy_schema_state SET revision = 'previous' WHERE id = 1"); err != nil {
		t.Fatalf("set previous revision: %v", err)
	}
	status, err := VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) || status.Current || status.PresentObjects != 1 {
		t.Fatalf("VerifySchema() with previous revision status = %#v, err = %v", status, err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM fairy_schema_state WHERE id = 1"); err != nil {
		t.Fatalf("delete schema revision: %v", err)
	}
	status, err = VerifySchema(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) || status.Current || status.PresentObjects != 0 {
		t.Fatalf("VerifySchema() without revision status = %#v, err = %v", status, err)
	}
}

func TestVerifySchemaFailsWhenPoolIsClosedIntegration(t *testing.T) {
	pool := openIsolatedPool(t, t.Context())
	pool.Close()
	if _, err := VerifySchema(t.Context(), pool); err == nil {
		t.Fatal("VerifySchema on a closed pool succeeded")
	}
}

func openIsolatedPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	adminConfig := ShortTimeoutConfig(databaseURL)
	admin, err := pgxpool.New(ctx, adminConfig.URL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("fairy_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanupPool, err := pgxpool.New(cleanupCtx, adminConfig.URL)
		if err != nil {
			t.Logf("open cleanup pool: %v", err)
			return
		}
		defer cleanupPool.Close()
		_, _ = cleanupPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	config := ShortTimeoutConfig(withSearchPath(t, databaseURL, schema))
	database, err := Open(ctx, config)
	if err != nil {
		t.Fatalf("open isolated pool: %v", err)
	}
	return database.Raw()
}

func withSearchPath(t *testing.T, rawURL string, schema string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	values := parsed.Query()
	values.Set("search_path", schema)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func assertRegclass(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, want bool) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("checking regclass %s: %v", name, err)
	}
	if exists != want {
		t.Fatalf("to_regclass(%s) exists = %v, want %v", name, exists, want)
	}
}

func assertColumnType(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, column string, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT data_type FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&got); err != nil {
		t.Fatalf("reading %s.%s type: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("%s.%s type = %s, want %s", table, column, got, want)
	}
}

func assertVectorColumnType(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, column string, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `
SELECT format_type(attribute.atttypid, attribute.atttypmod)
FROM pg_attribute AS attribute
JOIN pg_class AS relation ON relation.oid = attribute.attrelid
JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = current_schema()
  AND relation.relname = $1
  AND attribute.attname = $2
  AND NOT attribute.attisdropped`, table, column).Scan(&got); err != nil {
		t.Fatalf("read vector type for %s.%s: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("%s.%s type = %q, want %q", table, column, got, want)
	}
}

func assertColumnAbsent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, column string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM information_schema.columns
  WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
)`, table, column).Scan(&exists); err != nil {
		t.Fatalf("checking column %s.%s: %v", table, column, err)
	}
	if exists {
		t.Fatalf("column %s.%s still exists", table, column)
	}
}

func assertConstraint(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, name string, want bool) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS (
	SELECT 1
	FROM pg_constraint c
	JOIN pg_class t ON t.oid = c.conrelid
	JOIN pg_namespace n ON n.oid = t.relnamespace
	WHERE n.nspname = current_schema() AND t.relname = $1 AND c.conname = $2
)`, table, name).Scan(&exists); err != nil {
		t.Fatalf("checking constraint %s.%s: %v", table, name, err)
	}
	if exists != want {
		t.Fatalf("constraint %s.%s exists = %v, want %v", table, name, exists, want)
	}
}

func catalogFingerprint(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(ctx, testSchemaCatalogQuery)
	if err != nil {
		t.Fatalf("read catalog fingerprint: %v", err)
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var kind, owner, name string
		if err := rows.Scan(&kind, &owner, &name); err != nil {
			t.Fatalf("scan catalog fingerprint: %v", err)
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\n", kind, owner, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate catalog fingerprint: %v", err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

const testSchemaCatalogQuery = `
SELECT 'table' AS kind, tablename AS owner, '' AS name
FROM pg_tables
WHERE schemaname = current_schema()
UNION ALL
SELECT 'column', table_name, column_name
FROM information_schema.columns
WHERE table_schema = current_schema()
UNION ALL
SELECT 'constraint', t.relname, c.conname
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
UNION ALL
SELECT 'index', '', indexname
FROM pg_indexes
WHERE schemaname = current_schema()
ORDER BY 1, 2, 3`
