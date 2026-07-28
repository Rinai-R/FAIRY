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

var postgresForeignKeys = []schemaConstraint{
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
	{"extraction_batches", "extraction_batches_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"extraction_batch_turns", "extraction_batch_turns_batch_fk", "FOREIGN KEY (batch_id) REFERENCES extraction_batches(id) ON DELETE CASCADE"},
	{"extraction_batch_turns", "extraction_batch_turns_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE RESTRICT"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"endpoint_conversations", "endpoint_conversations_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"social_memory_entries", "social_memory_entries_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"social_reply_feedback", "social_reply_feedback_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"social_reply_feedback", "social_reply_feedback_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"social_person_notes", "social_person_notes_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
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
	{"memory_embedding_jobs_status", "CREATE INDEX IF NOT EXISTS memory_embedding_jobs_status ON memory_embedding_jobs(status, lease_expires_at_ms ASC NULLS FIRST, updated_at_ms ASC, id ASC)"},
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
		if err := tx.AutoMigrate(schemaModels()...); err != nil {
			return fmt.Errorf("auto-migrating PostgreSQL schema: %w", err)
		}
		for _, constraint := range postgresForeignKeys {
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
