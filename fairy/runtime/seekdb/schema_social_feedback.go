package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const socialMemoryFeedbackEventsTableName = "social_memory_feedback_events"

// socialMemoryFeedbackEventsSchema is immutable revision 9. It adds the
// body-free social feedback audit ledger that revision 7 explicitly left
// out of the cognitive direct-record tables.
var socialMemoryFeedbackEventsSchema = schemaTable{
	name: socialMemoryFeedbackEventsTableName,
	ddl: `CREATE TABLE IF NOT EXISTS social_memory_feedback_events (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  character_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  entry_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  adoption VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  outcome VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  credit VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  evidence_message_ids JSON NOT NULL,
  observed_message_count INT UNSIGNED NOT NULL,
  evaluator_revision VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY social_memory_feedback_events_turn_entry_key (turn_id, entry_id),
  KEY social_memory_feedback_events_conversation_created_idx (conversation_id, created_at_ms, id),
  KEY social_memory_feedback_events_entry_created_idx (entry_id, created_at_ms, id),
  CONSTRAINT social_memory_feedback_events_conversation_fk FOREIGN KEY (conversation_id, character_id)
    REFERENCES conversations (id, character_id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT social_memory_feedback_events_turn_fk FOREIGN KEY (conversation_id, turn_id)
    REFERENCES conversation_turns (conversation_id, id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT social_memory_feedback_events_entry_fk FOREIGN KEY (entry_id, character_id, conversation_id)
    REFERENCES social_memory_entries (id, character_id, conversation_id)
    ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT social_memory_feedback_events_invariants_check CHECK (
    CHAR_LENGTH(id) BETWEEN 1 AND 128 AND id = TRIM(id) AND
    CHAR_LENGTH(character_id) BETWEEN 1 AND 128 AND character_id = TRIM(character_id) AND
    CHAR_LENGTH(conversation_id) BETWEEN 1 AND 128 AND conversation_id = TRIM(conversation_id) AND
    CHAR_LENGTH(turn_id) BETWEEN 1 AND 128 AND turn_id = TRIM(turn_id) AND
    CHAR_LENGTH(entry_id) BETWEEN 1 AND 128 AND entry_id = TRIM(entry_id) AND
    adoption IN ('adopted', 'not_adopted', 'uncertain') AND
    outcome IN ('positive', 'partial', 'negative', 'unknown') AND
    credit IN ('entry', 'execution', 'context', 'unknown') AND
    ((adoption = 'adopted') OR (outcome = 'unknown' AND credit = 'unknown')) AND
    ((outcome <> 'unknown') OR credit = 'unknown') AND
    JSON_TYPE(evidence_message_ids) = 'ARRAY' AND
    JSON_LENGTH(evidence_message_ids) <= 6 AND
    ((outcome = 'unknown') = (JSON_LENGTH(evidence_message_ids) = 0)) AND
    observed_message_count BETWEEN 0 AND 6 AND
    CHAR_LENGTH(evaluator_revision) BETWEEN 1 AND 128 AND
      evaluator_revision = TRIM(evaluator_revision) AND
    created_at_ms > 0
  )
)`,
	columns: []schemaColumn{
		{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
		{name: "character_id", columnType: "varchar(128)", collation: "ascii_bin"},
		{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
		{name: "turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
		{name: "entry_id", columnType: "varchar(128)", collation: "ascii_bin"},
		{name: "adoption", columnType: "varchar(16)", collation: "ascii_bin"},
		{name: "outcome", columnType: "varchar(16)", collation: "ascii_bin"},
		{name: "credit", columnType: "varchar(16)", collation: "ascii_bin"},
		{name: "evidence_message_ids", columnType: "json"},
		{name: "observed_message_count", columnType: "int unsigned"},
		{name: "evaluator_revision", columnType: "varchar(128)", collation: "ascii_bin"},
		{name: "created_at_ms", columnType: "bigint unsigned"},
	},
	indexes: []schemaIndex{
		ascendingBTreeIndex("PRIMARY", true, "id"),
		ascendingBTreeIndex("social_memory_feedback_events_turn_entry_key", true, "turn_id", "entry_id"),
		ascendingBTreeIndex("social_memory_feedback_events_conversation_created_idx", false,
			"conversation_id", "created_at_ms", "id"),
		ascendingBTreeIndex("social_memory_feedback_events_entry_created_idx", false,
			"entry_id", "created_at_ms", "id"),
	},
	checks: []schemaCheck{{
		name: "social_memory_feedback_events_invariants_check",
		clause: "(((CHAR_LENGTH(`id`) >= 1) and (CHAR_LENGTH(`id`) <= 128)) and (`id` = trim(`id`)) and " +
			"((CHAR_LENGTH(`character_id`) >= 1) and (CHAR_LENGTH(`character_id`) <= 128)) and (`character_id` = trim(`character_id`)) and " +
			"((CHAR_LENGTH(`conversation_id`) >= 1) and (CHAR_LENGTH(`conversation_id`) <= 128)) and (`conversation_id` = trim(`conversation_id`)) and " +
			"((CHAR_LENGTH(`turn_id`) >= 1) and (CHAR_LENGTH(`turn_id`) <= 128)) and (`turn_id` = trim(`turn_id`)) and " +
			"((CHAR_LENGTH(`entry_id`) >= 1) and (CHAR_LENGTH(`entry_id`) <= 128)) and (`entry_id` = trim(`entry_id`)) and " +
			"(`adoption` in ('adopted','not_adopted','uncertain')) and " +
			"(`outcome` in ('positive','partial','negative','unknown')) and " +
			"(`credit` in ('entry','execution','context','unknown')) and " +
			"((`adoption` = 'adopted') or ((`outcome` = 'unknown') and (`credit` = 'unknown'))) and " +
			"((`outcome` <> 'unknown') or (`credit` = 'unknown')) and " +
			"(JSON_TYPE(`evidence_message_ids`) = 'ARRAY') and (JSON_LENGTH(`evidence_message_ids`) <= 6) and " +
			"((`outcome` = 'unknown') = (JSON_LENGTH(`evidence_message_ids`) = 0)) and " +
			"((`observed_message_count` >= 0) and (`observed_message_count` <= 6)) and " +
			"((CHAR_LENGTH(`evaluator_revision`) >= 1) and (CHAR_LENGTH(`evaluator_revision`) <= 128)) and " +
			"(`evaluator_revision` = trim(`evaluator_revision`)) and (`created_at_ms` > 0))",
	}},
	foreignKeys: []schemaForeignKey{
		{
			name: "social_memory_feedback_events_conversation_fk", referencedTable: "conversations",
			updateRule: "restrict", deleteRule: "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "id", sameSchema: true},
				{name: "character_id", referencedColumn: "character_id", sameSchema: true},
			},
		},
		{
			name: "social_memory_feedback_events_turn_fk", referencedTable: "conversation_turns",
			updateRule: "restrict", deleteRule: "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "conversation_id", sameSchema: true},
				{name: "turn_id", referencedColumn: "id", sameSchema: true},
			},
		},
		{
			name: "social_memory_feedback_events_entry_fk", referencedTable: "social_memory_entries",
			updateRule: "restrict", deleteRule: "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "entry_id", referencedColumn: "id", sameSchema: true},
				{name: "character_id", referencedColumn: "character_id", sameSchema: true},
				{name: "conversation_id", referencedColumn: "conversation_id", sameSchema: true},
			},
		},
	},
}

func socialFeedbackEventsSchemaChecksum() [sha256.Size]byte {
	return schemaDDLChecksum([]string{socialMemoryFeedbackEventsSchema.ddl})
}

func applySocialFeedbackEventsSchema(ctx context.Context, connection *sql.Conn) error {
	if _, err := connection.ExecContext(ctx, socialMemoryFeedbackEventsSchema.ddl); err != nil {
		return fmt.Errorf("create SeekDB social memory feedback events table: %w", err)
	}
	return nil
}

func verifySocialFeedbackEventsSchema(ctx context.Context, connection *sql.Conn) error {
	if err := verifyDuplicateRevalidationSchema(ctx, connection); err != nil {
		return err
	}
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB social feedback CHECK enforcement metadata: %w", err)
	}
	return verifySchemaTable(ctx, connection, socialMemoryFeedbackEventsSchema, enforcedAvailable)
}
