package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const (
	stickersTableName             = "stickers"
	expressionDeliveriesTableName = "expression_deliveries"
)

// stickerCatalogSchema is immutable revision 10. Catalog metadata lives in
// SeekDB; image bytes stay in content files named by SHA-256. Expression
// delivery results are a body-free ledger keyed by conversation/turn/beat.
var stickerCatalogSchema = [...]schemaTable{
	{
		name: stickersTableName,
		ddl: `CREATE TABLE IF NOT EXISTS stickers (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  content_sha256 BINARY(32) NOT NULL,
  mime_type VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  byte_count INT UNSIGNED NOT NULL,
  description VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  tags JSON NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY stickers_content_sha256_key (content_sha256),
  KEY stickers_status_updated_idx (status, updated_at_ms, id),
  CONSTRAINT stickers_invariants_check CHECK (
    CHAR_LENGTH(id) BETWEEN 1 AND 128 AND id = TRIM(id) AND
    mime_type IN ('image/jpeg', 'image/png', 'image/gif', 'image/webp') AND
    byte_count BETWEEN 1 AND 5242880 AND
    CHAR_LENGTH(description) <= 512 AND description = TRIM(description) AND
    JSON_TYPE(tags) = 'ARRAY' AND JSON_LENGTH(tags) <= 16 AND
    status IN ('draft', 'active', 'disabled') AND
    (status <> 'active' OR CHAR_LENGTH(description) > 0) AND
    created_at_ms > 0 AND
    updated_at_ms >= created_at_ms
  )
)`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "content_sha256", columnType: "binary(32)"},
			{name: "mime_type", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "byte_count", columnType: "int unsigned"},
			{name: "description", columnType: "varchar(512)", collation: "utf8mb4_bin"},
			{name: "tags", columnType: "json"},
			{name: "status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex("stickers_content_sha256_key", true, "content_sha256"),
			ascendingBTreeIndex("stickers_status_updated_idx", false, "status", "updated_at_ms", "id"),
		},
		checks: []schemaCheck{{
			name: "stickers_invariants_check",
			clause: "(((CHAR_LENGTH(`id`) >= 1) and (CHAR_LENGTH(`id`) <= 128)) and (`id` = trim(`id`)) and " +
				"(`mime_type` in ('image/jpeg','image/png','image/gif','image/webp')) and " +
				"((`byte_count` >= 1) and (`byte_count` <= 5242880)) and " +
				"(CHAR_LENGTH(`description`) <= 512) and (`description` = trim(`description`)) and " +
				"(JSON_TYPE(`tags`) = 'ARRAY') and (JSON_LENGTH(`tags`) <= 16) and " +
				"(`status` in ('draft','active','disabled')) and " +
				"((`status` <> 'active') or (CHAR_LENGTH(`description`) > 0)) and " +
				"(`created_at_ms` > 0) and (`updated_at_ms` >= `created_at_ms`))",
		}},
	},
	{
		name: expressionDeliveriesTableName,
		ddl: `CREATE TABLE IF NOT EXISTS expression_deliveries (
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  beat_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  external_message_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  error_message LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (conversation_id, turn_id, beat_id),
  KEY expression_deliveries_turn_created_idx (conversation_id, turn_id, created_at_ms),
  CONSTRAINT expression_deliveries_turn_fk FOREIGN KEY (conversation_id, turn_id)
    REFERENCES conversation_turns (conversation_id, id)
    ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT expression_deliveries_invariants_check CHECK (
    CHAR_LENGTH(conversation_id) BETWEEN 1 AND 128 AND conversation_id = TRIM(conversation_id) AND
    CHAR_LENGTH(turn_id) BETWEEN 1 AND 128 AND turn_id = TRIM(turn_id) AND
    CHAR_LENGTH(beat_id) BETWEEN 1 AND 128 AND beat_id = TRIM(beat_id) AND
      beat_id NOT REGEXP '[[:cntrl:]]' AND
    status IN ('succeeded', 'failed') AND
    ((status = 'succeeded') = (error_message IS NULL)) AND
    ((status = 'failed') = (error_message IS NOT NULL AND CHAR_LENGTH(error_message) > 0)) AND
    (status = 'succeeded' OR external_message_id IS NULL) AND
    (external_message_id IS NULL OR
      (CHAR_LENGTH(external_message_id) BETWEEN 1 AND 128 AND
        external_message_id = TRIM(external_message_id) AND
        external_message_id NOT REGEXP '[[:cntrl:]]')) AND
    created_at_ms > 0
  )
)`,
		columns: []schemaColumn{
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "beat_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "external_message_id", columnType: "varchar(128)", nullable: true, collation: "utf8mb4_bin"},
			{name: "error_message", columnType: "longtext", nullable: true, collation: "utf8mb4_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "conversation_id", "turn_id", "beat_id"),
			ascendingBTreeIndex("expression_deliveries_turn_created_idx", false,
				"conversation_id", "turn_id", "created_at_ms"),
		},
		checks: []schemaCheck{{
			name: "expression_deliveries_invariants_check",
			clause: "(((CHAR_LENGTH(`conversation_id`) >= 1) and (CHAR_LENGTH(`conversation_id`) <= 128)) and (`conversation_id` = trim(`conversation_id`)) and " +
				"((CHAR_LENGTH(`turn_id`) >= 1) and (CHAR_LENGTH(`turn_id`) <= 128)) and (`turn_id` = trim(`turn_id`)) and " +
				"((CHAR_LENGTH(`beat_id`) >= 1) and (CHAR_LENGTH(`beat_id`) <= 128)) and (`beat_id` = trim(`beat_id`)) and " +
				"(not((`beat_id` regexp '[[:cntrl:]]'))) and " +
				"(`status` in ('succeeded','failed')) and " +
				"((`status` = 'succeeded') = (`error_message` is null)) and " +
				"((`status` = 'failed') = ((`error_message` is not null) and (CHAR_LENGTH(`error_message`) > 0))) and " +
				"((`status` = 'succeeded') or (`external_message_id` is null)) and " +
				"((`external_message_id` is null) or (((CHAR_LENGTH(`external_message_id`) >= 1) and (CHAR_LENGTH(`external_message_id`) <= 128)) and " +
				"(`external_message_id` = trim(`external_message_id`)) and (not((`external_message_id` regexp '[[:cntrl:]]'))))) and " +
				"(`created_at_ms` > 0))",
		}},
		foreignKeys: []schemaForeignKey{{
			name: "expression_deliveries_turn_fk", referencedTable: "conversation_turns",
			updateRule: "restrict", deleteRule: "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "conversation_id", sameSchema: true},
				{name: "turn_id", referencedColumn: "id", sameSchema: true},
			},
		}},
	},
}

func stickerCatalogSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(stickerCatalogSchema))
	for _, table := range stickerCatalogSchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func applyStickerCatalogSchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range stickerCatalogSchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB %s table: %w", table.name, err)
		}
	}
	return nil
}

func verifyStickerCatalogSchema(ctx context.Context, connection *sql.Conn) error {
	if err := verifySocialFeedbackEventsSchema(ctx, connection); err != nil {
		return err
	}
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB sticker catalog CHECK enforcement metadata: %w", err)
	}
	for _, table := range stickerCatalogSchema {
		if err := verifySchemaTable(ctx, connection, table, enforcedAvailable); err != nil {
			return err
		}
	}
	return nil
}
