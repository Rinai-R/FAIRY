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

const knowledgeIngestSourcesJSONConstraintDefinition = `CHECK (
  CASE jsonb_typeof(sources_json)
    WHEN 'object' THEN batch_id <> ''
    WHEN 'array' THEN batch_id = '' OR jsonb_array_length(sources_json) BETWEEN 1 AND 5
    ELSE FALSE
  END
)`

const knowledgeIngestTaskJSONConstraintDefinition = `CHECK (
  (
    task_id = ''
    AND source_json = '{}'::jsonb
    AND status IN ('succeeded', 'failed', 'dropped')
  )
  OR (
    task_id <> ''
    AND jsonb_typeof(source_json) = 'object'
    AND source_json <> '{}'::jsonb
  )
)`

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
	{"personal_memory_evidence", "personal_memory_evidence_memory_fk", "FOREIGN KEY (memory_id) REFERENCES personal_memories(id) ON DELETE CASCADE"},
	{"personal_memory_evidence", "personal_memory_evidence_turn_evidence_fk", "FOREIGN KEY (turn_id, evidence_id) REFERENCES conversation_turn_evidence(turn_id, evidence_id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_source_conversation_fk", "FOREIGN KEY (source_conversation_id) REFERENCES conversations(id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_source_turn_fk", "FOREIGN KEY (source_turn_id) REFERENCES conversation_turns(id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_supersedes_fk", "FOREIGN KEY (supersedes_id) REFERENCES knowledge_entries(id) ON DELETE RESTRICT"},
	{"knowledge_sources", "knowledge_sources_knowledge_fk", "FOREIGN KEY (knowledge_id) REFERENCES knowledge_entries(id) ON DELETE CASCADE"},
	{"knowledge_document_versions", "knowledge_document_versions_document_fk", "FOREIGN KEY (document_id) REFERENCES knowledge_documents(id) ON DELETE CASCADE"},
	{"knowledge_chunks", "knowledge_chunks_version_fk", "FOREIGN KEY (version_id) REFERENCES knowledge_document_versions(id) ON DELETE CASCADE"},
	{"knowledge_evidence", "knowledge_evidence_knowledge_fk", "FOREIGN KEY (knowledge_id) REFERENCES knowledge_entries(id) ON DELETE CASCADE"},
	{"knowledge_evidence", "knowledge_evidence_chunk_fk", "FOREIGN KEY (chunk_id) REFERENCES knowledge_chunks(id) ON DELETE CASCADE"},
	{"knowledge_evidence", "knowledge_evidence_version_fk", "FOREIGN KEY (version_id) REFERENCES knowledge_document_versions(id) ON DELETE CASCADE"},
	{"extraction_batches", "extraction_batches_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"extraction_batch_turns", "extraction_batch_turns_batch_fk", "FOREIGN KEY (batch_id) REFERENCES extraction_batches(id) ON DELETE CASCADE"},
	{"extraction_batch_turns", "extraction_batch_turns_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE RESTRICT"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_sources_json_check", knowledgeIngestSourcesJSONConstraintDefinition},
	{"knowledge_sources", "knowledge_sources_canonical_url_check", "CHECK (canonical_url = '' OR canonical_url ~ '^https?://[^[:space:]]+$')"},
	{"endpoint_conversations", "endpoint_conversations_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"social_memory_entries", "social_memory_entries_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"social_reply_feedback", "social_reply_feedback_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"social_reply_feedback", "social_reply_feedback_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"social_person_notes", "social_person_notes_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
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
	{"extraction_batches_one_running", "CREATE UNIQUE INDEX IF NOT EXISTS extraction_batches_one_running ON extraction_batches(conversation_id) WHERE status = 'running'"},
	{"extraction_batches_claimable", "CREATE INDEX IF NOT EXISTS extraction_batches_claimable ON extraction_batches(status, lease_expires_at_ms ASC NULLS FIRST, updated_at_ms ASC, id ASC) WHERE status IN ('pending', 'running')"},
	{"knowledge_ingest_jobs_status", "CREATE INDEX IF NOT EXISTS knowledge_ingest_jobs_status ON knowledge_ingest_jobs(status, lease_expires_at_ms ASC NULLS FIRST, created_at_ms ASC, id ASC)"},
	{"knowledge_ingest_jobs_retry", "CREATE INDEX IF NOT EXISTS knowledge_ingest_jobs_retry ON knowledge_ingest_jobs(status, next_attempt_at_ms ASC, updated_at_ms ASC, id ASC) WHERE status IN ('waiting_turn', 'pending', 'running')"},
	{"knowledge_ingest_jobs_batch_key", "CREATE UNIQUE INDEX IF NOT EXISTS knowledge_ingest_jobs_batch_key ON knowledge_ingest_jobs(batch_id) WHERE batch_id <> ''"},
	{"knowledge_ingest_jobs_task_key", "CREATE UNIQUE INDEX IF NOT EXISTS knowledge_ingest_jobs_task_key ON knowledge_ingest_jobs(task_id) WHERE task_id <> ''"},
	{"knowledge_sources_canonical_key", "CREATE UNIQUE INDEX IF NOT EXISTS knowledge_sources_canonical_key ON knowledge_sources(knowledge_id, canonical_url) WHERE canonical_url <> ''"},
	{"knowledge_entries_active_fact_key", "CREATE UNIQUE INDEX IF NOT EXISTS knowledge_entries_active_fact_key ON knowledge_entries(fact_key) WHERE fact_key IS NOT NULL AND status = 'verified'"},
	{"knowledge_evidence_active_version", "CREATE INDEX IF NOT EXISTS knowledge_evidence_active_version ON knowledge_evidence(version_id, knowledge_id) WHERE active"},
	{"knowledge_chunks_embedding_hnsw", "CREATE INDEX IF NOT EXISTS knowledge_chunks_embedding_hnsw ON knowledge_chunks USING hnsw (embedding public.vector_cosine_ops) WHERE embedding IS NOT NULL"},
	{"personal_memories_embedding_hnsw", "CREATE INDEX IF NOT EXISTS personal_memories_embedding_hnsw ON personal_memories USING hnsw (embedding public.vector_cosine_ops) WHERE embedding IS NOT NULL AND status = 'active' AND review_status = 'ready'"},
	{"knowledge_entries_embedding_hnsw", "CREATE INDEX IF NOT EXISTS knowledge_entries_embedding_hnsw ON knowledge_entries USING hnsw (embedding public.vector_cosine_ops) WHERE embedding IS NOT NULL AND status = 'verified'"},
	{"social_memory_entries_situation_trgm", "CREATE INDEX IF NOT EXISTS social_memory_entries_situation_trgm ON social_memory_entries USING gin (situation public.gin_trgm_ops)"},
	{"social_memory_entries_content_trgm", "CREATE INDEX IF NOT EXISTS social_memory_entries_content_trgm ON social_memory_entries USING gin (content public.gin_trgm_ops)"},
	{"social_memory_entries_recall_trgm", "CREATE INDEX IF NOT EXISTS social_memory_entries_recall_trgm ON social_memory_entries USING gin (recall_cue public.gin_trgm_ops)"},
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
		if err := replaceKnowledgeIngestInvariant(tx); err != nil {
			return err
		}
		if err := replaceKnowledgeIngestSourcesJSONConstraint(tx); err != nil {
			return err
		}
		if err := migrateKnowledgeIngestTaskColumns(tx); err != nil {
			return err
		}
		if err := replaceKnowledgeIngestTaskJSONConstraint(tx); err != nil {
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

func replaceKnowledgeIngestInvariant(tx *gorm.DB) error {
	if err := tx.Exec("ALTER TABLE knowledge_ingest_jobs DROP CONSTRAINT IF EXISTS knowledge_ingest_jobs_invariants_check").Error; err != nil {
		return fmt.Errorf("dropping legacy knowledge ingest invariant: %w", err)
	}
	if err := tx.Exec(`ALTER TABLE knowledge_ingest_jobs ADD CONSTRAINT knowledge_ingest_jobs_invariants_check CHECK (
  rank >= 0
  AND fetched_at_ms >= 0
  AND status IN ('waiting_turn', 'pending', 'running', 'succeeded', 'failed', 'dropped')
  AND (lease_expires_at_ms IS NULL OR lease_expires_at_ms >= 0)
  AND attempt_count >= 0
  AND next_attempt_at_ms >= 0
  AND created_at_ms >= 0
  AND updated_at_ms >= created_at_ms
  AND ((lease_owner IS NULL) = (lease_expires_at_ms IS NULL))
  AND (status = 'running' OR lease_owner IS NULL)
)`).Error; err != nil {
		return fmt.Errorf("creating knowledge ingest invariant: %w", err)
	}
	return nil
}

func replaceKnowledgeIngestSourcesJSONConstraint(tx *gorm.DB) error {
	if err := tx.Exec("ALTER TABLE knowledge_ingest_jobs DROP CONSTRAINT IF EXISTS knowledge_ingest_jobs_sources_json_check").Error; err != nil {
		return fmt.Errorf("dropping legacy knowledge ingest source payload constraint: %w", err)
	}
	statement := "ALTER TABLE knowledge_ingest_jobs ADD CONSTRAINT knowledge_ingest_jobs_sources_json_check " +
		knowledgeIngestSourcesJSONConstraintDefinition
	if err := tx.Exec(statement).Error; err != nil {
		return fmt.Errorf("creating knowledge ingest source payload constraint: %w", err)
	}
	return nil
}

func migrateKnowledgeIngestTaskColumns(tx *gorm.DB) error {
	if err := tx.Exec(`
WITH legacy_candidates AS (
  SELECT
    id,
    CASE
      WHEN batch_id <> '' THEN batch_id
      WHEN batch_id = '' AND url ~ '^https?://[^[:space:]]+$' AND rank BETWEEN 1 AND 5
        THEN 'legacy-' || id
      ELSE ''
    END AS migrated_task_id,
    CASE
      WHEN batch_id <> '' AND jsonb_typeof(sources_json) = 'object'
        THEN sources_json
      WHEN batch_id <> '' AND jsonb_typeof(sources_json) = 'array'
        AND jsonb_array_length(sources_json) = 1
        THEN sources_json -> 0
      WHEN batch_id = '' AND url ~ '^https?://[^[:space:]]+$' AND rank BETWEEN 1 AND 5
        THEN jsonb_build_object(
          'id', 'legacy-source-' || id,
          'title', title,
          'url', url,
          'snippet', snippet,
          'rank', rank,
          'fetchedAtUnixMs', fetched_at_ms
        )
      ELSE NULL
    END AS migrated_source
  FROM knowledge_ingest_jobs
  WHERE task_id = ''
),
valid_candidates AS (
  SELECT id, migrated_task_id, migrated_source
  FROM legacy_candidates
  WHERE migrated_task_id <> ''
    AND jsonb_typeof(migrated_source) = 'object'
    AND migrated_source - ARRAY['id', 'title', 'url', 'snippet', 'rank', 'fetchedAtUnixMs'] = '{}'::jsonb
    AND jsonb_typeof(migrated_source -> 'id') = 'string'
    AND migrated_source ->> 'id' <> ''
    AND jsonb_typeof(migrated_source -> 'title') = 'string'
    AND jsonb_typeof(migrated_source -> 'url') = 'string'
    AND migrated_source ->> 'url' ~ '^https?://[^[:space:]]+$'
    AND jsonb_typeof(migrated_source -> 'snippet') = 'string'
    AND (migrated_source ->> 'title' <> '' OR migrated_source ->> 'snippet' <> '')
    AND jsonb_typeof(migrated_source -> 'rank') = 'number'
    AND migrated_source ->> 'rank' ~ '^[1-5]$'
    AND jsonb_typeof(migrated_source -> 'fetchedAtUnixMs') = 'number'
    AND migrated_source ->> 'fetchedAtUnixMs' ~ '^[0-9]+$'
)
UPDATE knowledge_ingest_jobs AS jobs
SET task_id = candidates.migrated_task_id,
    source_json = candidates.migrated_source,
    status = CASE WHEN jobs.status = 'running' THEN 'pending' ELSE jobs.status END,
    attempt_count = CASE
      WHEN jobs.status = 'running' THEN GREATEST(0, jobs.attempt_count - 1)
      ELSE jobs.attempt_count
    END,
    lease_owner = CASE WHEN jobs.status = 'running' THEN NULL ELSE jobs.lease_owner END,
    lease_expires_at_ms = CASE WHEN jobs.status = 'running' THEN NULL ELSE jobs.lease_expires_at_ms END,
    next_attempt_at_ms = CASE WHEN jobs.status = 'running' THEN 0 ELSE jobs.next_attempt_at_ms END,
    updated_at_ms = CASE WHEN jobs.status = 'running' THEN GREATEST(jobs.updated_at_ms, 0) ELSE jobs.updated_at_ms END
FROM valid_candidates AS candidates
WHERE jobs.id = candidates.id
`).Error; err != nil {
		return fmt.Errorf("migrating singular knowledge ingest tasks: %w", err)
	}
	if err := tx.Exec(`
UPDATE knowledge_ingest_jobs
SET status = 'failed',
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    next_attempt_at_ms = 0,
    error_category = 'legacy_payload_not_singular',
    error_message = 'legacy knowledge ingest payload cannot be represented as one task',
    updated_at_ms = GREATEST(updated_at_ms, 0)
WHERE task_id = ''
  AND status IN ('waiting_turn', 'pending', 'running')
`).Error; err != nil {
		return fmt.Errorf("settling non-singular legacy knowledge ingest jobs: %w", err)
	}
	return nil
}

func replaceKnowledgeIngestTaskJSONConstraint(tx *gorm.DB) error {
	if err := tx.Exec("ALTER TABLE knowledge_ingest_jobs DROP CONSTRAINT IF EXISTS knowledge_ingest_jobs_task_json_check").Error; err != nil {
		return fmt.Errorf("dropping knowledge ingest task payload constraint: %w", err)
	}
	statement := "ALTER TABLE knowledge_ingest_jobs ADD CONSTRAINT knowledge_ingest_jobs_task_json_check " +
		knowledgeIngestTaskJSONConstraintDefinition
	if err := tx.Exec(statement).Error; err != nil {
		return fmt.Errorf("creating knowledge ingest task payload constraint: %w", err)
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
