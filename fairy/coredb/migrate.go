// Package coredb owns FAIRY's PostgreSQL connection, migration, and readiness.
package coredb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const migrationLockKey int64 = 0x46414952595f4442

var (
	ErrSchemaAbsent     = errors.New("postgres schema is absent")
	ErrSchemaNotCurrent = errors.New("postgres schema is not current")
)

// SchemaStatus reports readiness of the single committed schema revision.
// Object counters remain for API compatibility; they count only that marker,
// not tables, columns, constraints, or indexes.
type SchemaStatus struct {
	ExpectedObjects int      `json:"expectedObjects"`
	PresentObjects  int      `json:"presentObjects"`
	MissingObjects  []string `json:"missingObjects,omitempty"`
	Current         bool     `json:"current"`
}

type schemaConstraint struct {
	Table      string
	Name       string
	Definition string
}

type schemaIndex struct {
	Name string
	DDL  string
}

var postgresConstraints = []schemaConstraint{
	{"conversation_turns", "conversation_turns_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"conversation_turn_evidence", "conversation_turn_evidence_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"conversation_messages", "conversation_messages_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"conversation_messages", "conversation_messages_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"prompt_windows", "prompt_windows_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"turn_runtime_events", "turn_runtime_events_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"turn_runtime_events", "turn_runtime_events_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"tool_executions", "tool_executions_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"tool_executions", "tool_executions_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"lane_continuations", "lane_continuations_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"context_windows", "context_windows_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"personal_memories", "personal_memories_source_conversation_fk", "FOREIGN KEY (source_conversation_id) REFERENCES conversations(id) ON DELETE RESTRICT"},
	{"personal_memories", "personal_memories_source_turn_fk", "FOREIGN KEY (source_turn_id) REFERENCES conversation_turns(id) ON DELETE RESTRICT"},
	{"personal_memories", "personal_memories_supersedes_fk", "FOREIGN KEY (supersedes_id) REFERENCES personal_memories(id) ON DELETE RESTRICT"},
	{"personal_memories", "personal_memories_evidence_ids_check", "CHECK (jsonb_typeof(evidence_ids_json) = 'array' AND jsonb_array_length(evidence_ids_json) <= 8)"},
	{"knowledge_entries", "knowledge_entries_source_conversation_fk", "FOREIGN KEY (source_conversation_id) REFERENCES conversations(id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_source_turn_fk", "FOREIGN KEY (source_turn_id) REFERENCES conversation_turns(id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_supersedes_fk", "FOREIGN KEY (supersedes_id) REFERENCES knowledge_entries(id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_document_fk", "FOREIGN KEY (document_id) REFERENCES knowledge_documents(id) ON DELETE SET NULL"},
	{"knowledge_entries", "knowledge_entries_direct_evidence_check", "CHECK ((document_id IS NULL AND evidence_text = '') OR (document_id IS NOT NULL AND evidence_text <> ''))"},
	{"endpoint_conversations", "endpoint_conversations_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"social_memory_entries", "social_memory_entries_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"feedback_events", "feedback_events_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"feedback_events", "feedback_events_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"personal_memories", "personal_memories_embedding_check", "CHECK ((embedding_model_id IS NULL AND embedding_content_hash IS NULL AND embedding IS NULL) OR (embedding_model_id <> '' AND embedding_content_hash ~ '^[0-9a-f]{64}$' AND embedding IS NOT NULL))"},
	{"knowledge_entries", "knowledge_entries_embedding_check", "CHECK ((embedding_model_id IS NULL AND embedding_content_hash IS NULL AND embedding IS NULL) OR (embedding_model_id <> '' AND embedding_content_hash ~ '^[0-9a-f]{64}$' AND embedding IS NOT NULL))"},
}

var postgresIndexes = []schemaIndex{
	{"conversation_turns_extraction", "CREATE INDEX IF NOT EXISTS conversation_turns_extraction ON conversation_turns(conversation_id, extraction_state, sequence ASC) WHERE status = 'completed'"},
	{"stickers_description_trgm", "CREATE INDEX IF NOT EXISTS stickers_description_trgm ON stickers USING gin (description public.gin_trgm_ops)"},
	{"tool_executions_pending_deadline", "CREATE INDEX IF NOT EXISTS tool_executions_pending_deadline ON tool_executions(deadline_at_ms ASC, created_at_ms ASC, id ASC) WHERE status = 'pending'"},
	{"personal_memories_content_trgm", "CREATE INDEX IF NOT EXISTS personal_memories_content_trgm ON personal_memories USING gin (content public.gin_trgm_ops)"},
	{"knowledge_entries_topic_trgm", "CREATE INDEX IF NOT EXISTS knowledge_entries_topic_trgm ON knowledge_entries USING gin (topic public.gin_trgm_ops)"},
	{"knowledge_entries_statement_trgm", "CREATE INDEX IF NOT EXISTS knowledge_entries_statement_trgm ON knowledge_entries USING gin (statement public.gin_trgm_ops)"},
	{"personal_memories_embedding_hnsw", "CREATE INDEX IF NOT EXISTS personal_memories_embedding_hnsw ON personal_memories USING hnsw (embedding public.vector_cosine_ops) WHERE embedding IS NOT NULL AND status = 'active' AND review_status = 'ready'"},
	{"knowledge_entries_embedding_hnsw", "CREATE INDEX IF NOT EXISTS knowledge_entries_embedding_hnsw ON knowledge_entries USING hnsw (embedding public.vector_cosine_ops) WHERE embedding IS NOT NULL AND status = 'verified'"},
	{"social_memory_entries_situation_trgm", "CREATE INDEX IF NOT EXISTS social_memory_entries_situation_trgm ON social_memory_entries USING gin (situation public.gin_trgm_ops)"},
	{"social_memory_entries_content_trgm", "CREATE INDEX IF NOT EXISTS social_memory_entries_content_trgm ON social_memory_entries USING gin (content public.gin_trgm_ops)"},
	{"social_memory_entries_recall_trgm", "CREATE INDEX IF NOT EXISTS social_memory_entries_recall_trgm ON social_memory_entries USING gin (recall_cue public.gin_trgm_ops)"},
	{"feedback_events_social_reply_turn_key", "CREATE UNIQUE INDEX IF NOT EXISTS feedback_events_social_reply_turn_key ON feedback_events(turn_id) WHERE type = 'social_reply_feedback'"},
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db, closeDB, err := openGORM(pool)
	if err != nil {
		return err
	}
	defer closeDB()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationLockKey).Error; err != nil {
			return fmt.Errorf("acquiring migration advisory lock: %w", err)
		}
		if err := tx.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public").Error; err != nil {
			return fmt.Errorf("creating pg_trgm extension: %w", err)
		}
		if err := tx.Exec("CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public").Error; err != nil {
			return fmt.Errorf("creating pgvector extension: %w", err)
		}
		if err := tx.AutoMigrate(schemaModels()...); err != nil {
			return fmt.Errorf("auto-migrating PostgreSQL schema: %w", err)
		}
		if err := migrateDirectMemoryKnowledgeSchema(tx); err != nil {
			return err
		}
		for _, constraint := range postgresConstraints {
			exists, err := constraintExists(tx, constraint.Table, constraint.Name)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			statement := fmt.Sprintf(
				"ALTER TABLE %s ADD CONSTRAINT %s %s",
				pgx.Identifier{constraint.Table}.Sanitize(),
				pgx.Identifier{constraint.Name}.Sanitize(),
				constraint.Definition,
			)
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("creating constraint %s: %w", constraint.Name, err)
			}
		}
		for _, index := range postgresIndexes {
			if err := tx.Exec(index.DDL).Error; err != nil {
				return fmt.Errorf("creating index %s: %w", index.Name, err)
			}
		}
		state := schemaState{ID: 1, Revision: currentSchemaRevision}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"revision"}),
		}).Create(&state).Error; err != nil {
			return fmt.Errorf("committing PostgreSQL schema revision: %w", err)
		}
		return nil
	})
}

func migrateDirectMemoryKnowledgeSchema(tx *gorm.DB) error {
	var legacy bool
	if err := tx.Raw(`SELECT to_regclass(current_schema() || '.knowledge_document_versions') IS NOT NULL`).Scan(&legacy).Error; err != nil {
		return fmt.Errorf("checking legacy memory schema: %w", err)
	}
	if !legacy {
		return nil
	}
	statements := []struct {
		name string
		sql  string
	}{
		{
			name: "projecting personal memory evidence",
			sql: `
UPDATE personal_memories AS memory
SET evidence_ids_json = evidence.ids
FROM (
  SELECT memory_id, jsonb_agg(evidence_id ORDER BY created_at_ms, evidence_id) AS ids
  FROM personal_memory_evidence
  GROUP BY memory_id
) AS evidence
WHERE memory.id = evidence.memory_id`,
		},
		{
			name: "projecting social person notes",
			sql: `
INSERT INTO social_memory_entries(
  id, character_id, conversation_id, kind, situation, content, recall_cue,
  content_hash, sender_id, sender_name, status, source_start_ms, source_end_ms,
  use_count, positive_count, negative_count, unknown_count, created_at_ms, updated_at_ms
)
SELECT
  id, character_id, conversation_id, 'person_note',
  COALESCE(NULLIF(sender_name, ''), sender_id),
  note,
  COALESCE(NULLIF(sender_name, ''), sender_id),
  encode(sha256(convert_to(note, 'UTF8')), 'hex'),
  sender_id, sender_name, 'active',
  GREATEST(created_at_ms, 1), GREATEST(updated_at_ms, created_at_ms, 1),
  0, 0, 0, 0, created_at_ms, updated_at_ms
FROM social_person_notes
ON CONFLICT (character_id, conversation_id, sender_id)
DO UPDATE SET
  situation = EXCLUDED.situation,
  content = EXCLUDED.content,
  recall_cue = EXCLUDED.recall_cue,
  content_hash = EXCLUDED.content_hash,
  sender_name = EXCLUDED.sender_name,
  updated_at_ms = GREATEST(social_memory_entries.updated_at_ms, EXCLUDED.updated_at_ms)`,
		},
		{
			name: "projecting current knowledge documents",
			sql: `
WITH current_documents AS (
  SELECT
    document.id,
    version.content_hash,
    version.content_type,
    version.fetched_at_ms,
    version.etag,
    version.last_modified,
    version.reconciler_revision,
    COALESCE(string_agg(chunk.text, '' ORDER BY chunk.ordinal), '') AS content
  FROM knowledge_documents AS document
  JOIN knowledge_document_versions AS version ON version.id = document.current_version_id
  LEFT JOIN knowledge_chunks AS chunk ON chunk.version_id = version.id
  GROUP BY document.id, version.content_hash, version.content_type, version.fetched_at_ms,
           version.etag, version.last_modified, version.reconciler_revision
)
UPDATE knowledge_documents AS document
SET content = current.content,
    content_hash = current.content_hash,
    content_type = current.content_type,
    fetched_at_ms = current.fetched_at_ms,
    etag = current.etag,
    last_modified = current.last_modified,
    reconciler_revision = current.reconciler_revision
FROM current_documents AS current
WHERE document.id = current.id`,
		},
		{
			name: "projecting direct knowledge evidence",
			sql: `
WITH ranked_evidence AS (
  SELECT
    evidence.knowledge_id,
    version.document_id,
    chunk.text,
    row_number() OVER (
      PARTITION BY evidence.knowledge_id
      ORDER BY version.fetched_at_ms DESC, evidence.created_at_ms DESC, chunk.id
    ) AS rank
  FROM knowledge_evidence AS evidence
  JOIN knowledge_chunks AS chunk ON chunk.id = evidence.chunk_id
  JOIN knowledge_document_versions AS version ON version.id = evidence.version_id
  WHERE evidence.active
)
UPDATE knowledge_entries AS knowledge
SET document_id = evidence.document_id,
    evidence_text = evidence.text
FROM ranked_evidence AS evidence
WHERE knowledge.id = evidence.knowledge_id
  AND evidence.rank = 1`,
		},
		{
			name: "projecting personal feedback events",
			sql: `
INSERT INTO feedback_events(
  id, type, conversation_id, turn_id, character_id, payload_json, status,
  attempt_count, next_attempt_at_ms, created_at_ms, updated_at_ms
)
SELECT
  'personal-' || turn.turn_id,
  'personal_memory',
  batch.conversation_id,
  turn.turn_id,
  batch.character_id,
  '{}'::jsonb,
  'pending',
  CASE WHEN batch.status = 'running' THEN GREATEST(0, batch.attempt_count - 1) ELSE batch.attempt_count END,
  0,
  batch.created_at_ms,
  batch.updated_at_ms
FROM extraction_batches AS batch
JOIN extraction_batch_turns AS turn ON turn.batch_id = batch.id
WHERE batch.status IN ('pending', 'running')
ON CONFLICT (id) DO NOTHING`,
		},
		{
			name: "projecting web knowledge feedback events",
			sql: `
INSERT INTO feedback_events(
  id, type, conversation_id, turn_id, character_id, payload_json, status,
  attempt_count, next_attempt_at_ms, error_category, error_message, created_at_ms, updated_at_ms
)
SELECT
  job.id,
  'web_knowledge',
  job.conversation_id,
  job.turn_id,
  conversation.character_id,
  jsonb_build_object('taskId', job.task_id, 'source', job.source_json),
  CASE WHEN job.status = 'running' THEN 'pending' ELSE job.status END,
  CASE WHEN job.status = 'running' THEN GREATEST(0, job.attempt_count - 1) ELSE job.attempt_count END,
  CASE WHEN job.status = 'running' THEN 0 ELSE job.next_attempt_at_ms END,
  job.error_category,
  job.error_message,
  job.created_at_ms,
  job.updated_at_ms
FROM knowledge_ingest_jobs AS job
JOIN conversations AS conversation ON conversation.id = job.conversation_id
WHERE job.status IN ('waiting_turn', 'pending', 'running')
  AND job.task_id <> ''
  AND jsonb_typeof(job.source_json) = 'object'
  AND job.source_json <> '{}'::jsonb
ON CONFLICT (id) DO NOTHING`,
		},
		{
			name: "releasing projected extraction turns",
			sql: `
UPDATE conversation_turns
SET extraction_state = 'pending'
WHERE extraction_state = 'claimed'
  AND id IN (
    SELECT turn_id
    FROM feedback_events
    WHERE type = 'personal_memory' AND status = 'pending'
  )`,
		},
		{
			name: "dropping legacy memory tables",
			sql: `
DROP TABLE social_reply_feedback;
DROP TABLE social_person_notes;
DROP TABLE personal_memory_evidence;
DROP TABLE knowledge_evidence;
DROP TABLE knowledge_sources;
DROP TABLE knowledge_chunks;
DROP TABLE knowledge_document_versions;
DROP TABLE extraction_batch_turns;
DROP TABLE extraction_batches;
DROP TABLE knowledge_ingest_jobs`,
		},
		{
			name: "dropping legacy knowledge columns",
			sql: `
ALTER TABLE knowledge_documents
  DROP COLUMN current_version_id,
  DROP COLUMN current_content_hash;
ALTER TABLE knowledge_entries
  DROP COLUMN subject,
  DROP COLUMN predicate,
  DROP COLUMN value,
  DROP COLUMN fact_key`,
		},
		{
			name: "replacing direct record invariants",
			sql: `
ALTER TABLE knowledge_documents DROP CONSTRAINT IF EXISTS knowledge_documents_invariants_check;
ALTER TABLE knowledge_documents ADD CONSTRAINT knowledge_documents_invariants_check CHECK (
  canonical_url ~ '^https?://[^[:space:]]+$'
  AND (content_hash = '' OR content_hash ~ '^[0-9a-f]{64}$')
  AND (reconciler_revision = '' OR reconciler_revision ~ '^[0-9a-f]{64}$')
  AND fetched_at_ms >= 0
  AND created_at_ms >= 0
  AND updated_at_ms >= created_at_ms
);
ALTER TABLE social_memory_entries DROP CONSTRAINT IF EXISTS social_memory_entries_invariants_check;
ALTER TABLE social_memory_entries ADD CONSTRAINT social_memory_entries_invariants_check CHECK (
  kind IN ('episode', 'expression', 'behavior', 'person_note')
  AND situation <> ''
  AND content <> ''
  AND recall_cue <> ''
  AND content_hash ~ '^[0-9a-f]{64}$'
  AND status IN ('active', 'suppressed')
  AND source_start_ms > 0
  AND source_end_ms >= source_start_ms
  AND use_count >= 0
  AND positive_count >= 0
  AND negative_count >= 0
  AND unknown_count >= 0
  AND ((kind = 'person_note') = (sender_id IS NOT NULL))
  AND created_at_ms >= 0
  AND updated_at_ms >= created_at_ms
)`,
		},
	}
	for _, statement := range statements {
		if err := tx.Exec(statement.sql).Error; err != nil {
			return fmt.Errorf("%s: %w", statement.name, err)
		}
	}
	return nil
}

func VerifySchema(ctx context.Context, pool *pgxpool.Pool) (SchemaStatus, error) {
	status := SchemaStatus{ExpectedObjects: 1}
	if pool == nil {
		return status, errors.New("database pool is not open")
	}

	var revision string
	err := pool.QueryRow(ctx, `SELECT revision FROM fairy_schema_state WHERE id = 1`).Scan(&revision)
	if err != nil {
		var postgresError *pgconn.PgError
		switch {
		case errors.As(err, &postgresError) && postgresError.Code == "42P01":
			status.MissingObjects = []string{"table:fairy_schema_state"}
			return status, ErrSchemaAbsent
		case errors.Is(err, pgx.ErrNoRows):
			status.MissingObjects = []string{"revision:" + currentSchemaRevision}
			return status, fmt.Errorf("%w: current schema revision is not committed", ErrSchemaNotCurrent)
		case errors.As(err, &postgresError) && postgresError.Code == "42703":
			status.MissingObjects = []string{"column:fairy_schema_state.revision"}
			return status, fmt.Errorf("%w: schema revision column is absent", ErrSchemaNotCurrent)
		default:
			return status, fmt.Errorf("reading PostgreSQL schema revision: %w", err)
		}
	}

	status.PresentObjects = 1
	if revision != currentSchemaRevision {
		status.MissingObjects = []string{"revision:" + currentSchemaRevision}
		return status, fmt.Errorf("%w: have revision %q, want %q", ErrSchemaNotCurrent, revision, currentSchemaRevision)
	}
	status.Current = true
	return status, nil
}

func openGORM(pool *pgxpool.Pool) (*gorm.DB, func(), error) {
	if pool == nil {
		return nil, func() {}, errors.New("database pool is not open")
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		DisableAutomaticPing:                     true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, func() {}, fmt.Errorf("opening GORM database: %w", err)
	}
	return db, func() { _ = sqlDB.Close() }, nil
}

func constraintExists(db *gorm.DB, table string, name string) (bool, error) {
	var exists bool
	if err := db.Raw(`SELECT EXISTS (
SELECT 1 FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema() AND t.relname = ? AND c.conname = ?
)`, table, name).Scan(&exists).Error; err != nil {
		return false, fmt.Errorf("checking constraint %s: %w", name, err)
	}
	return exists, nil
}
