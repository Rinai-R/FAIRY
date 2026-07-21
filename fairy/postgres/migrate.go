package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const migrationLockKey int64 = 0x46414952595f4442

var (
	ErrSchemaAbsent     = errors.New("postgres schema is absent")
	ErrSchemaNotCurrent = errors.New("postgres schema is not current")
)

type SchemaStatus struct {
	ExpectedObjects   int      `json:"expectedObjects"`
	PresentObjects    int      `json:"presentObjects"`
	MissingObjects    []string `json:"missingObjects,omitempty"`
	UnexpectedObjects []string `json:"unexpectedObjects,omitempty"`
	Current           bool     `json:"current"`
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

var schemaConstraints = []schemaConstraint{
	{"conversations", "conversations_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"conversations", "conversations_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"conversation_turns", "conversation_turns_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"conversation_turns", "conversation_turns_sequence_check", "CHECK (sequence > 0)"},
	{"conversation_turns", "conversation_turns_status_check", "CHECK (status IN ('interpreting', 'planning', 'responding', 'completed', 'interrupted', 'failed'))"},
	{"conversation_turns", "conversation_turns_extraction_state_check", "CHECK (extraction_state IN ('ineligible', 'pending', 'claimed', 'processed'))"},
	{"conversation_turns", "conversation_turns_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"conversation_turns", "conversation_turns_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"conversation_turns", "conversation_turns_conversation_sequence_key", "UNIQUE (conversation_id, sequence)"},
	{"conversation_turns", "conversation_turns_error_check", "CHECK ((status = 'failed') = (error_code IS NOT NULL AND error_message IS NOT NULL))"},
	{"conversation_messages", "conversation_messages_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"conversation_messages", "conversation_messages_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"conversation_messages", "conversation_messages_sequence_check", "CHECK (sequence > 0)"},
	{"conversation_messages", "conversation_messages_role_check", "CHECK (role IN ('user', 'assistant'))"},
	{"conversation_messages", "conversation_messages_content_check", "CHECK (content <> '')"},
	{"conversation_messages", "conversation_messages_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"conversation_messages", "conversation_messages_conversation_sequence_key", "UNIQUE (conversation_id, sequence)"},
	{"conversation_messages", "conversation_messages_turn_role_key", "UNIQUE (turn_id, role)"},
	{"prompt_windows", "prompt_windows_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"prompt_windows", "prompt_windows_revision_check", "CHECK (revision > 0)"},
	{"prompt_windows", "prompt_windows_cutoff_check", "CHECK (cutoff_message_sequence >= 0)"},
	{"prompt_windows", "prompt_windows_updated_at_ms_check", "CHECK (updated_at_ms >= 0)"},
	{"turn_runtime_events", "turn_runtime_events_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"turn_runtime_events", "turn_runtime_events_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"turn_runtime_events", "turn_runtime_events_sequence_check", "CHECK (sequence > 0)"},
	{"turn_runtime_events", "turn_runtime_events_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"turn_runtime_events", "turn_runtime_events_conversation_turn_sequence_key", "UNIQUE (conversation_id, turn_id, sequence)"},
	{"lane_continuations", "lane_continuations_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"lane_continuations", "lane_continuations_window_revision_check", "CHECK (window_revision > 0)"},
	{"lane_continuations", "lane_continuations_updated_at_ms_check", "CHECK (updated_at_ms >= 0)"},
	{"context_windows", "context_windows_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"context_windows", "context_windows_window_number_check", "CHECK (window_number >= 0)"},
	{"context_windows", "context_windows_observed_tokens_check", "CHECK (observed_prefill_tokens IS NULL OR observed_prefill_tokens >= 0)"},
	{"context_windows", "context_windows_estimated_tokens_check", "CHECK (estimated_prefill_tokens IS NULL OR estimated_prefill_tokens >= 0)"},
	{"context_windows", "context_windows_failure_count_check", "CHECK (failure_count >= 0)"},
	{"context_windows", "context_windows_prompt_revision_check", "CHECK (prompt_window_revision > 0)"},
	{"context_windows", "context_windows_updated_at_ms_check", "CHECK (updated_at_ms >= 0)"},
	{"personal_memories", "personal_memories_scope_kind_check", "CHECK (scope_kind IN ('global', 'character', 'relationship', 'unassigned_legacy'))"},
	{"personal_memories", "personal_memories_content_check", "CHECK (content <> '')"},
	{"personal_memories", "personal_memories_status_check", "CHECK (status IN ('active', 'superseded', 'tombstone'))"},
	{"personal_memories", "personal_memories_confidence_check", "CHECK (confidence_basis_points BETWEEN 0 AND 10000)"},
	{"personal_memories", "personal_memories_source_conversation_fk", "FOREIGN KEY (source_conversation_id) REFERENCES conversations(id) ON DELETE RESTRICT"},
	{"personal_memories", "personal_memories_source_turn_fk", "FOREIGN KEY (source_turn_id) REFERENCES conversation_turns(id) ON DELETE RESTRICT"},
	{"personal_memories", "personal_memories_supersedes_fk", "FOREIGN KEY (supersedes_id) REFERENCES personal_memories(id) ON DELETE RESTRICT"},
	{"personal_memories", "personal_memories_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"personal_memories", "personal_memories_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"personal_memories", "personal_memories_character_scope_check", "CHECK ((scope_kind = 'character') = (character_id IS NOT NULL) OR scope_kind <> 'character')"},
	{"knowledge_entries", "knowledge_entries_topic_check", "CHECK (topic <> '')"},
	{"knowledge_entries", "knowledge_entries_statement_check", "CHECK (statement <> '')"},
	{"knowledge_entries", "knowledge_entries_status_check", "CHECK (status IN ('candidate', 'verified', 'superseded', 'rejected', 'tombstone'))"},
	{"knowledge_entries", "knowledge_entries_confidence_check", "CHECK (confidence_basis_points BETWEEN 0 AND 10000)"},
	{"knowledge_entries", "knowledge_entries_source_conversation_fk", "FOREIGN KEY (source_conversation_id) REFERENCES conversations(id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_source_turn_fk", "FOREIGN KEY (source_turn_id) REFERENCES conversation_turns(id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_supersedes_fk", "FOREIGN KEY (supersedes_id) REFERENCES knowledge_entries(id) ON DELETE RESTRICT"},
	{"knowledge_entries", "knowledge_entries_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"knowledge_entries", "knowledge_entries_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"knowledge_sources", "knowledge_sources_knowledge_fk", "FOREIGN KEY (knowledge_id) REFERENCES knowledge_entries(id) ON DELETE CASCADE"},
	{"knowledge_sources", "knowledge_sources_rank_check", "CHECK (rank >= 0)"},
	{"knowledge_sources", "knowledge_sources_fetched_at_ms_check", "CHECK (fetched_at_ms >= 0)"},
	{"extraction_batches", "extraction_batches_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"extraction_batches", "extraction_batches_status_check", "CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled'))"},
	{"extraction_batches", "extraction_batches_first_turn_check", "CHECK (first_turn_sequence > 0)"},
	{"extraction_batches", "extraction_batches_last_turn_check", "CHECK (last_turn_sequence >= first_turn_sequence)"},
	{"extraction_batches", "extraction_batches_lease_expires_check", "CHECK (lease_expires_at_ms IS NULL OR lease_expires_at_ms >= 0)"},
	{"extraction_batches", "extraction_batches_attempt_count_check", "CHECK (attempt_count >= 0)"},
	{"extraction_batches", "extraction_batches_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"extraction_batches", "extraction_batches_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"extraction_batches", "extraction_batches_lease_pair_check", "CHECK ((lease_owner IS NULL) = (lease_expires_at_ms IS NULL))"},
	{"extraction_batches", "extraction_batches_running_owner_check", "CHECK (status = 'running' OR lease_owner IS NULL)"},
	{"extraction_batch_turns", "extraction_batch_turns_batch_fk", "FOREIGN KEY (batch_id) REFERENCES extraction_batches(id) ON DELETE CASCADE"},
	{"extraction_batch_turns", "extraction_batch_turns_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE RESTRICT"},
	{"extraction_batch_turns", "extraction_batch_turns_sequence_check", "CHECK (turn_sequence > 0)"},
	{"extraction_batch_turns", "extraction_batch_turns_batch_sequence_key", "UNIQUE (batch_id, turn_sequence)"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_turn_fk", "FOREIGN KEY (turn_id) REFERENCES conversation_turns(id) ON DELETE CASCADE"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_rank_check", "CHECK (rank >= 0)"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_fetched_at_ms_check", "CHECK (fetched_at_ms >= 0)"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_status_check", "CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'dropped'))"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_lease_expires_check", "CHECK (lease_expires_at_ms IS NULL OR lease_expires_at_ms >= 0)"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_attempt_count_check", "CHECK (attempt_count >= 0)"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_lease_pair_check", "CHECK ((lease_owner IS NULL) = (lease_expires_at_ms IS NULL))"},
	{"knowledge_ingest_jobs", "knowledge_ingest_jobs_running_owner_check", "CHECK (status = 'running' OR lease_owner IS NULL)"},
	{"memory_embedding_items", "memory_embedding_items_kind_check", "CHECK (item_kind IN ('personal_memory', 'knowledge'))"},
	{"memory_embedding_items", "memory_embedding_items_dimensions_check", "CHECK (dimensions = 512)"},
	{"memory_embedding_items", "memory_embedding_items_content_hash_check", "CHECK (content_hash <> '')"},
	{"memory_embedding_items", "memory_embedding_items_status_check", "CHECK (status IN ('pending', 'embedded', 'failed'))"},
	{"memory_embedding_items", "memory_embedding_items_embedded_at_check", "CHECK (embedded_at_ms IS NULL OR embedded_at_ms >= 0)"},
	{"memory_embedding_items", "memory_embedding_items_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"memory_embedding_items", "memory_embedding_items_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"memory_embedding_items", "memory_embedding_items_item_key", "UNIQUE (item_kind, item_id, model_id)"},
	{"memory_embedding_items", "memory_embedding_items_point_key", "UNIQUE (point_id)"},
	{"memory_embedding_items", "memory_embedding_items_legacy_rowid_key", "UNIQUE (legacy_vector_rowid)"},
	{"memory_embedding_items", "memory_embedding_items_embedded_status_check", "CHECK ((status = 'embedded') = (embedded_at_ms IS NOT NULL))"},
	{"memory_embedding_jobs", "memory_embedding_jobs_kind_check", "CHECK (item_kind IN ('personal_memory', 'knowledge'))"},
	{"memory_embedding_jobs", "memory_embedding_jobs_dimensions_check", "CHECK (dimensions = 512)"},
	{"memory_embedding_jobs", "memory_embedding_jobs_content_hash_check", "CHECK (content_hash <> '')"},
	{"memory_embedding_jobs", "memory_embedding_jobs_status_check", "CHECK (status IN ('pending', 'running', 'succeeded', 'failed'))"},
	{"memory_embedding_jobs", "memory_embedding_jobs_lease_expires_check", "CHECK (lease_expires_at_ms IS NULL OR lease_expires_at_ms >= 0)"},
	{"memory_embedding_jobs", "memory_embedding_jobs_attempt_count_check", "CHECK (attempt_count >= 0)"},
	{"memory_embedding_jobs", "memory_embedding_jobs_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"memory_embedding_jobs", "memory_embedding_jobs_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"memory_embedding_jobs", "memory_embedding_jobs_item_content_key", "UNIQUE (item_kind, item_id, model_id, content_hash)"},
	{"memory_embedding_jobs", "memory_embedding_jobs_lease_pair_check", "CHECK ((lease_owner IS NULL) = (lease_expires_at_ms IS NULL))"},
	{"memory_embedding_jobs", "memory_embedding_jobs_running_owner_check", "CHECK (status = 'running' OR lease_owner IS NULL)"},
	{"secret_values", "secret_values_key_version_check", "CHECK (key_version > 0)"},
	{"secret_values", "secret_values_nonce_check", "CHECK (octet_length(nonce) = 12)"},
	{"secret_values", "secret_values_ciphertext_check", "CHECK (octet_length(ciphertext) > 0)"},
	{"secret_values", "secret_values_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"secret_values", "secret_values_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"vector_rebuild_runs", "vector_rebuild_runs_status_check", "CHECK (status IN ('pending', 'running', 'succeeded', 'failed'))"},
	{"vector_rebuild_runs", "vector_rebuild_runs_scanned_check", "CHECK (scanned_items >= 0)"},
	{"vector_rebuild_runs", "vector_rebuild_runs_upserted_check", "CHECK (upserted_points >= 0)"},
	{"vector_rebuild_runs", "vector_rebuild_runs_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"vector_rebuild_runs", "vector_rebuild_runs_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"vector_reconciliation_runs", "vector_reconciliation_runs_status_check", "CHECK (status IN ('running', 'succeeded', 'failed'))"},
	{"vector_reconciliation_runs", "vector_reconciliation_runs_missing_check", "CHECK (missing_points >= 0)"},
	{"vector_reconciliation_runs", "vector_reconciliation_runs_stale_check", "CHECK (stale_points >= 0)"},
	{"vector_reconciliation_runs", "vector_reconciliation_runs_orphan_check", "CHECK (orphan_points >= 0)"},
	{"vector_reconciliation_runs", "vector_reconciliation_runs_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"vector_reconciliation_runs", "vector_reconciliation_runs_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"surface_conversations", "surface_conversations_surface_check", "CHECK (surface IN ('desktop', 'im_private', 'im_group'))"},
	{"surface_conversations", "surface_conversations_digest_check", "CHECK (surface_key_digest ~ '^[0-9a-f]{64}$')"},
	{"surface_conversations", "surface_conversations_conversation_fk", "FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE"},
	{"surface_conversations", "surface_conversations_created_at_ms_check", "CHECK (created_at_ms >= 0)"},
	{"surface_conversations", "surface_conversations_updated_at_ms_check", "CHECK (updated_at_ms >= created_at_ms)"},
	{"surface_conversations", "surface_conversations_conversation_key", "UNIQUE (conversation_id)"},
}

var schemaIndexes = []schemaIndex{
	{"conversations_character_updated", "CREATE INDEX IF NOT EXISTS conversations_character_updated ON conversations(character_id, updated_at_ms DESC, id ASC)"},
	{"conversation_turns_conversation_status", "CREATE INDEX IF NOT EXISTS conversation_turns_conversation_status ON conversation_turns(conversation_id, status, sequence ASC)"},
	{"conversation_turns_extraction", "CREATE INDEX IF NOT EXISTS conversation_turns_extraction ON conversation_turns(conversation_id, extraction_state, sequence ASC) WHERE status = 'completed'"},
	{"conversation_messages_conversation_sequence", "CREATE INDEX IF NOT EXISTS conversation_messages_conversation_sequence ON conversation_messages(conversation_id, sequence ASC)"},
	{"turn_runtime_events_turn_sequence", "CREATE INDEX IF NOT EXISTS turn_runtime_events_turn_sequence ON turn_runtime_events(conversation_id, turn_id, sequence ASC)"},
	{"turn_runtime_events_type_created", "CREATE INDEX IF NOT EXISTS turn_runtime_events_type_created ON turn_runtime_events(event_type, created_at_ms ASC, sequence ASC)"},
	{"personal_memories_scope_status", "CREATE INDEX IF NOT EXISTS personal_memories_scope_status ON personal_memories(scope_kind, character_id, status, updated_at_ms DESC, id ASC)"},
	{"personal_memories_content_trgm", "CREATE INDEX IF NOT EXISTS personal_memories_content_trgm ON personal_memories USING gin (content public.gin_trgm_ops)"},
	{"knowledge_entries_status_updated", "CREATE INDEX IF NOT EXISTS knowledge_entries_status_updated ON knowledge_entries(status, updated_at_ms DESC, id ASC)"},
	{"knowledge_entries_topic_trgm", "CREATE INDEX IF NOT EXISTS knowledge_entries_topic_trgm ON knowledge_entries USING gin (topic public.gin_trgm_ops)"},
	{"knowledge_entries_statement_trgm", "CREATE INDEX IF NOT EXISTS knowledge_entries_statement_trgm ON knowledge_entries USING gin (statement public.gin_trgm_ops)"},
	{"extraction_batches_one_running", "CREATE UNIQUE INDEX IF NOT EXISTS extraction_batches_one_running ON extraction_batches(conversation_id) WHERE status = 'running'"},
	{"extraction_batches_claimable", "CREATE INDEX IF NOT EXISTS extraction_batches_claimable ON extraction_batches(status, lease_expires_at_ms ASC NULLS FIRST, updated_at_ms ASC, id ASC) WHERE status IN ('pending', 'running')"},
	{"knowledge_ingest_jobs_status", "CREATE INDEX IF NOT EXISTS knowledge_ingest_jobs_status ON knowledge_ingest_jobs(status, lease_expires_at_ms ASC NULLS FIRST, created_at_ms ASC, id ASC)"},
	{"memory_embedding_items_item", "CREATE INDEX IF NOT EXISTS memory_embedding_items_item ON memory_embedding_items(item_kind, item_id, model_id)"},
	{"memory_embedding_items_status", "CREATE INDEX IF NOT EXISTS memory_embedding_items_status ON memory_embedding_items(status, updated_at_ms ASC, id ASC)"},
	{"memory_embedding_jobs_status", "CREATE INDEX IF NOT EXISTS memory_embedding_jobs_status ON memory_embedding_jobs(status, lease_expires_at_ms ASC NULLS FIRST, updated_at_ms ASC, id ASC)"},
	{"memory_embedding_jobs_item", "CREATE INDEX IF NOT EXISTS memory_embedding_jobs_item ON memory_embedding_jobs(item_kind, item_id, model_id, content_hash)"},
	{"vector_rebuild_runs_status", "CREATE INDEX IF NOT EXISTS vector_rebuild_runs_status ON vector_rebuild_runs(status, updated_at_ms DESC, id ASC)"},
	{"vector_reconciliation_runs_status", "CREATE INDEX IF NOT EXISTS vector_reconciliation_runs_status ON vector_reconciliation_runs(status, updated_at_ms DESC, id ASC)"},
	{"surface_conversations_conversation", "CREATE INDEX IF NOT EXISTS surface_conversations_conversation ON surface_conversations(conversation_id)"},
}

func Migrate(ctx context.Context, pool *Pool) error {
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
		presentTables, totalTables, err := schemaTableCounts(tx)
		if err != nil {
			return err
		}
		if totalTables != 0 && (presentTables != len(schemaTableNames()) || totalTables != len(schemaTableNames())) {
			return fmt.Errorf("%w: GORM migration requires an empty or exact current table set, found %d required and %d total tables", ErrSchemaNotCurrent, presentTables, totalTables)
		}
		if totalTables == 0 {
			if err := tx.AutoMigrate(schemaModels()...); err != nil {
				return fmt.Errorf("auto-migrating PostgreSQL schema: %w", err)
			}
		}
		for _, constraint := range schemaConstraints {
			exists, err := constraintExists(tx, constraint.Table, constraint.Name)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			statement := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s", quoteIdentifier(constraint.Table), quoteIdentifier(constraint.Name), constraint.Definition)
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("creating constraint %s: %w", constraint.Name, err)
			}
		}
		for _, index := range schemaIndexes {
			if err := tx.Exec(index.DDL).Error; err != nil {
				return fmt.Errorf("creating index %s: %w", index.Name, err)
			}
		}
		return nil
	})
}

func schemaTableCounts(db *gorm.DB) (int, int, error) {
	present := 0
	for _, table := range schemaTableNames() {
		var exists bool
		if err := db.Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&exists).Error; err != nil {
			return 0, 0, fmt.Errorf("checking table %s: %w", table, err)
		}
		if exists {
			present++
		}
	}
	var total int
	if err := db.Raw("SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()").Scan(&total).Error; err != nil {
		return 0, 0, fmt.Errorf("counting current schema tables: %w", err)
	}
	return present, total, nil
}

func VerifySchema(ctx context.Context, pool *Pool) (SchemaStatus, error) {
	if pool == nil || pool.Raw() == nil {
		return SchemaStatus{}, errors.New("database pool is not open")
	}
	db, closeDB, err := openGORM(pool)
	if err != nil {
		return SchemaStatus{}, err
	}
	defer closeDB()
	db = db.WithContext(ctx)

	columnCount := 0
	for _, model := range schemaModels() {
		statement := &gorm.Statement{DB: db}
		if err := statement.Parse(model); err != nil {
			return SchemaStatus{}, fmt.Errorf("parsing schema model: %w", err)
		}
		columnCount += len(statement.Schema.DBNames)
	}
	expected := len(schemaModels()) + columnCount + len(schemaConstraints) + len(schemaIndexes) + 1
	status := SchemaStatus{ExpectedObjects: expected}
	presentTables := 0

	var extensionExists bool
	if err := pool.Raw().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')").Scan(&extensionExists); err != nil {
		return status, fmt.Errorf("checking pg_trgm extension: %w", err)
	}
	if extensionExists {
		status.PresentObjects++
	} else {
		status.MissingObjects = append(status.MissingObjects, "extension:pg_trgm")
	}

	for i, table := range schemaTableNames() {
		var exists bool
		if err := pool.Raw().QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
			return status, fmt.Errorf("checking table %s: %w", table, err)
		}
		if exists {
			status.PresentObjects++
			presentTables++
		} else {
			status.MissingObjects = append(status.MissingObjects, "table:"+table)
		}
		statement := &gorm.Statement{DB: db}
		if err := statement.Parse(schemaModels()[i]); err != nil {
			return status, fmt.Errorf("parsing schema model %s: %w", table, err)
		}
		for _, column := range statement.Schema.DBNames {
			if exists && db.Migrator().HasColumn(table, column) {
				status.PresentObjects++
			} else {
				status.MissingObjects = append(status.MissingObjects, "column:"+table+"."+column)
			}
		}
	}
	for _, constraint := range schemaConstraints {
		var exists bool
		if err := pool.Raw().QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema() AND t.relname = $1 AND c.conname = $2
)`, constraint.Table, constraint.Name).Scan(&exists); err != nil {
			return status, fmt.Errorf("checking constraint %s: %w", constraint.Name, err)
		}
		if exists {
			status.PresentObjects++
		} else {
			status.MissingObjects = append(status.MissingObjects, "constraint:"+constraint.Table+"."+constraint.Name)
		}
	}
	for _, index := range schemaIndexes {
		var exists bool
		if err := pool.Raw().QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", index.Name).Scan(&exists); err != nil {
			return status, fmt.Errorf("checking index %s: %w", index.Name, err)
		}
		if exists {
			status.PresentObjects++
		} else {
			status.MissingObjects = append(status.MissingObjects, "index:"+index.Name)
		}
	}
	rows, err := pool.Raw().Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename")
	if err != nil {
		return status, fmt.Errorf("listing current schema tables: %w", err)
	}
	defer rows.Close()
	expectedTables := make(map[string]bool, len(schemaTableNames()))
	for _, table := range schemaTableNames() {
		expectedTables[table] = true
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return status, fmt.Errorf("scanning current schema table: %w", err)
		}
		if !expectedTables[table] {
			status.UnexpectedObjects = append(status.UnexpectedObjects, "table:"+table)
		}
	}
	if err := rows.Err(); err != nil {
		return status, fmt.Errorf("iterating current schema tables: %w", err)
	}
	if presentTables == 0 {
		return status, ErrSchemaAbsent
	}
	if status.PresentObjects != status.ExpectedObjects || len(status.UnexpectedObjects) != 0 {
		if len(status.UnexpectedObjects) != 0 {
			return status, fmt.Errorf("%w: unexpected %s", ErrSchemaNotCurrent, status.UnexpectedObjects[0])
		}
		return status, fmt.Errorf("%w: missing %s (found %d of %d required objects)", ErrSchemaNotCurrent, status.MissingObjects[0], status.PresentObjects, status.ExpectedObjects)
	}
	status.Current = true
	return status, nil
}

func openGORM(pool *Pool) (*gorm.DB, func(), error) {
	if pool == nil || pool.Raw() == nil {
		return nil, func() {}, errors.New("database pool is not open")
	}
	sqlDB := stdlib.OpenDBFromPool(pool.Raw())
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
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

func quoteIdentifier(identifier string) string {
	quoted := `"`
	for _, char := range identifier {
		if char == '"' {
			quoted += `""`
			continue
		}
		quoted += string(char)
	}
	return quoted + `"`
}
