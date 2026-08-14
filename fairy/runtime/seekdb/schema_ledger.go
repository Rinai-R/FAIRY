package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const toolExecutionsTableName = "tool_executions"

// toolExecutionLedgerSchema is immutable revision 11. Tool executions keep
// metadata and hashes only; raw capture bytes stay outside SeekDB. Model and
// cache usage continue to live in turn_runtime_events metadata.
var toolExecutionLedgerSchema = [...]schemaTable{
	{
		name: toolExecutionsTableName,
		ddl: `CREATE TABLE IF NOT EXISTS tool_executions (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  call_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tool_name VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  deadline_at_ms BIGINT UNSIGNED NOT NULL,
  attempt_count INT UNSIGNED NOT NULL,
  last_dispatched_at_ms BIGINT UNSIGNED NULL,
  error_code VARCHAR(256) CHARACTER SET ascii COLLATE ascii_bin NULL,
  error_message VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  result_media_type VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
  result_width INT UNSIGNED NULL,
  result_height INT UNSIGNED NULL,
  result_byte_count INT UNSIGNED NULL,
  result_sha256 VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY tool_executions_turn_call_key (conversation_id, turn_id, call_id),
  UNIQUE KEY tool_executions_turn_tool_key (conversation_id, turn_id, tool_name),
  KEY tool_executions_status_deadline_idx (status, deadline_at_ms, created_at_ms, id),
  KEY tool_executions_turn_status_idx (conversation_id, turn_id, status, created_at_ms),
  CONSTRAINT tool_executions_turn_fk FOREIGN KEY (conversation_id, turn_id)
    REFERENCES conversation_turns (conversation_id, id)
    ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT tool_executions_invariants_check CHECK (
    CHAR_LENGTH(id) BETWEEN 1 AND 128 AND id = TRIM(id) AND
    CHAR_LENGTH(conversation_id) BETWEEN 1 AND 128 AND conversation_id = TRIM(conversation_id) AND
    CHAR_LENGTH(turn_id) BETWEEN 1 AND 128 AND turn_id = TRIM(turn_id) AND
    CHAR_LENGTH(call_id) BETWEEN 1 AND 128 AND call_id = TRIM(call_id) AND
    tool_name = 'desktop_observe' AND
    status IN ('pending', 'completed', 'failed', 'cancelled') AND
    deadline_at_ms > created_at_ms AND
    created_at_ms > 0 AND
    updated_at_ms >= created_at_ms AND
    (last_dispatched_at_ms IS NULL OR last_dispatched_at_ms >= created_at_ms) AND
    ((status = 'completed') = (
      result_media_type IS NOT NULL AND result_width IS NOT NULL AND
      result_height IS NOT NULL AND result_byte_count IS NOT NULL AND result_sha256 IS NOT NULL)) AND
    ((status IN ('failed', 'cancelled')) = (error_code IS NOT NULL AND error_message IS NOT NULL)) AND
    (status <> 'completed' OR (error_code IS NULL AND error_message IS NULL)) AND
    (result_media_type IS NULL OR result_media_type IN ('image/png', 'image/jpeg')) AND
    (result_width IS NULL OR result_width > 0) AND
    (result_height IS NULL OR result_height > 0) AND
    (result_byte_count IS NULL OR (result_byte_count BETWEEN 1 AND 786432)) AND
    (result_sha256 IS NULL OR result_sha256 REGEXP '^[0-9a-f]{64}$') AND
    (error_code IS NULL OR (CHAR_LENGTH(error_code) BETWEEN 1 AND 256 AND error_code = TRIM(error_code) AND
      error_code NOT REGEXP '[[:cntrl:]]')) AND
    (error_message IS NULL OR (CHAR_LENGTH(error_message) BETWEEN 1 AND 512 AND error_message = TRIM(error_message) AND
      error_message NOT REGEXP '[[:cntrl:]]'))
  )
)`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "call_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "tool_name", columnType: "varchar(32)", collation: "ascii_bin"},
			{name: "status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "deadline_at_ms", columnType: "bigint unsigned"},
			{name: "attempt_count", columnType: "int unsigned"},
			{name: "last_dispatched_at_ms", columnType: "bigint unsigned", nullable: true},
			{name: "error_code", columnType: "varchar(256)", nullable: true, collation: "ascii_bin"},
			{name: "error_message", columnType: "varchar(512)", nullable: true, collation: "utf8mb4_bin"},
			{name: "result_media_type", columnType: "varchar(16)", nullable: true, collation: "ascii_bin"},
			{name: "result_width", columnType: "int unsigned", nullable: true},
			{name: "result_height", columnType: "int unsigned", nullable: true},
			{name: "result_byte_count", columnType: "int unsigned", nullable: true},
			{name: "result_sha256", columnType: "varchar(64)", nullable: true, collation: "ascii_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex("tool_executions_turn_call_key", true, "conversation_id", "turn_id", "call_id"),
			ascendingBTreeIndex("tool_executions_turn_tool_key", true, "conversation_id", "turn_id", "tool_name"),
			ascendingBTreeIndex("tool_executions_status_deadline_idx", false,
				"status", "deadline_at_ms", "created_at_ms", "id"),
			ascendingBTreeIndex("tool_executions_turn_status_idx", false,
				"conversation_id", "turn_id", "status", "created_at_ms"),
		},
		checks: []schemaCheck{{
			name: "tool_executions_invariants_check",
			clause: "(((CHAR_LENGTH(`id`) >= 1) and (CHAR_LENGTH(`id`) <= 128)) and (`id` = trim(`id`)) and " +
				"((CHAR_LENGTH(`conversation_id`) >= 1) and (CHAR_LENGTH(`conversation_id`) <= 128)) and (`conversation_id` = trim(`conversation_id`)) and " +
				"((CHAR_LENGTH(`turn_id`) >= 1) and (CHAR_LENGTH(`turn_id`) <= 128)) and (`turn_id` = trim(`turn_id`)) and " +
				"((CHAR_LENGTH(`call_id`) >= 1) and (CHAR_LENGTH(`call_id`) <= 128)) and (`call_id` = trim(`call_id`)) and " +
				"(`tool_name` = 'desktop_observe') and " +
				"(`status` in ('pending','completed','failed','cancelled')) and " +
				"(`deadline_at_ms` > `created_at_ms`) and (`created_at_ms` > 0) and (`updated_at_ms` >= `created_at_ms`) and " +
				"((`last_dispatched_at_ms` is null) or (`last_dispatched_at_ms` >= `created_at_ms`)) and " +
				"((`status` = 'completed') = ((`result_media_type` is not null) and (`result_width` is not null) and " +
				"(`result_height` is not null) and (`result_byte_count` is not null) and (`result_sha256` is not null))) and " +
				"((`status` in ('failed','cancelled')) = ((`error_code` is not null) and (`error_message` is not null))) and " +
				"((`status` <> 'completed') or ((`error_code` is null) and (`error_message` is null))) and " +
				"((`result_media_type` is null) or (`result_media_type` in ('image/png','image/jpeg'))) and " +
				"((`result_width` is null) or (`result_width` > 0)) and " +
				"((`result_height` is null) or (`result_height` > 0)) and " +
				"((`result_byte_count` is null) or ((`result_byte_count` >= 1) and (`result_byte_count` <= 786432))) and " +
				"((`result_sha256` is null) or (`result_sha256` regexp '^[0-9a-f]{64}$')) and " +
				"((`error_code` is null) or (((CHAR_LENGTH(`error_code`) >= 1) and (CHAR_LENGTH(`error_code`) <= 256)) and " +
				"(`error_code` = trim(`error_code`)) and (not((`error_code` regexp '[[:cntrl:]]'))))) and " +
				"((`error_message` is null) or (((CHAR_LENGTH(`error_message`) >= 1) and (CHAR_LENGTH(`error_message`) <= 512)) and " +
				"(`error_message` = trim(`error_message`)) and (not((`error_message` regexp '[[:cntrl:]]'))))))",
		}},
		foreignKeys: []schemaForeignKey{{
			name: "tool_executions_turn_fk", referencedTable: "conversation_turns",
			updateRule: "restrict", deleteRule: "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "conversation_id", sameSchema: true},
				{name: "turn_id", referencedColumn: "id", sameSchema: true},
			},
		}},
	},
}

func toolExecutionLedgerSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(toolExecutionLedgerSchema))
	for _, table := range toolExecutionLedgerSchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func applyToolExecutionLedgerSchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range toolExecutionLedgerSchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB %s table: %w", table.name, err)
		}
	}
	return nil
}

func verifyToolExecutionLedgerSchema(ctx context.Context, connection *sql.Conn) error {
	if err := verifyStickerCatalogSchema(ctx, connection); err != nil {
		return err
	}
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB tool execution ledger CHECK enforcement metadata: %w", err)
	}
	for _, table := range toolExecutionLedgerSchema {
		if err := verifySchemaTable(ctx, connection, table, enforcedAvailable); err != nil {
			return err
		}
	}
	return nil
}
