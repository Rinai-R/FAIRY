package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const (
	pluginInstanceStateTableName      = "plugin_instance_state"
	pluginInstanceStatsTableName      = "plugin_instance_stats"
	pluginUpgradeJournalTableName     = "plugin_upgrade_journal"
	pluginInstanceConfigRefsTableName = "plugin_instance_config_refs"
)

// pluginPersistenceSchema is immutable revision 13. Host-owned plugin
// packages and instances already exist in revision 1. This revision adds
// per-instance state KV, call statistics, upgrade journal, and secret-handle
// config references. Guests never receive SQL.
var pluginPersistenceSchema = [...]schemaTable{
	{
		name: pluginInstanceStateTableName,
		ddl: `CREATE TABLE IF NOT EXISTS plugin_instance_state (
  instance_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  state_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  value LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (instance_id, state_key),
  KEY plugin_instance_state_updated_idx (instance_id, updated_at_ms, state_key),
  CONSTRAINT plugin_instance_state_instance_fk FOREIGN KEY (instance_id)
    REFERENCES plugin_instances (instance_id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT plugin_instance_state_invariants_check CHECK (
    CHAR_LENGTH(instance_id) BETWEEN 1 AND 128 AND instance_id = TRIM(instance_id) AND
    CHAR_LENGTH(state_key) BETWEEN 1 AND 128 AND state_key = TRIM(state_key) AND
    state_key REGEXP '^[a-z0-9][a-z0-9._-]{0,127}$' AND
    updated_at_ms > 0
  )
)`,
		columns: []schemaColumn{
			{name: "instance_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "state_key", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "value", columnType: "longtext", collation: "utf8mb4_bin"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "instance_id", "state_key"),
			ascendingBTreeIndex("plugin_instance_state_updated_idx", false, "instance_id", "updated_at_ms", "state_key"),
		},
		checks: []schemaCheck{{
			name: "plugin_instance_state_invariants_check",
			clause: "(((CHAR_LENGTH(`instance_id`) >= 1) and (CHAR_LENGTH(`instance_id`) <= 128)) and (`instance_id` = trim(`instance_id`)) and " +
				"((CHAR_LENGTH(`state_key`) >= 1) and (CHAR_LENGTH(`state_key`) <= 128)) and (`state_key` = trim(`state_key`)) and " +
				"(`state_key` regexp '^[a-z0-9][a-z0-9._-]{0,127}$') and (`updated_at_ms` > 0))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "plugin_instance_state_instance_fk",
			referencedTable: "plugin_instances",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns:         []schemaForeignKeyColumn{{name: "instance_id", referencedColumn: "instance_id", sameSchema: true}},
		}},
	},
	{
		name: pluginInstanceStatsTableName,
		ddl: `CREATE TABLE IF NOT EXISTS plugin_instance_stats (
  instance_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  guest_calls BIGINT UNSIGNED NOT NULL,
  host_calls BIGINT UNSIGNED NOT NULL,
  last_error_code VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (instance_id),
  CONSTRAINT plugin_instance_stats_instance_fk FOREIGN KEY (instance_id)
    REFERENCES plugin_instances (instance_id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT plugin_instance_stats_invariants_check CHECK (
    CHAR_LENGTH(instance_id) BETWEEN 1 AND 128 AND instance_id = TRIM(instance_id) AND
    (last_error_code IS NULL OR (
      CHAR_LENGTH(last_error_code) BETWEEN 1 AND 32 AND last_error_code = TRIM(last_error_code))) AND
    updated_at_ms > 0
  )
)`,
		columns: []schemaColumn{
			{name: "instance_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "guest_calls", columnType: "bigint unsigned"},
			{name: "host_calls", columnType: "bigint unsigned"},
			{name: "last_error_code", columnType: "varchar(32)", nullable: true, collation: "ascii_bin"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "instance_id"),
		},
		checks: []schemaCheck{{
			name: "plugin_instance_stats_invariants_check",
			clause: "(((CHAR_LENGTH(`instance_id`) >= 1) and (CHAR_LENGTH(`instance_id`) <= 128)) and (`instance_id` = trim(`instance_id`)) and " +
				"((`last_error_code` is null) or (((CHAR_LENGTH(`last_error_code`) >= 1) and (CHAR_LENGTH(`last_error_code`) <= 32)) and (`last_error_code` = trim(`last_error_code`)))) and " +
				"(`updated_at_ms` > 0))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "plugin_instance_stats_instance_fk",
			referencedTable: "plugin_instances",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns:         []schemaForeignKeyColumn{{name: "instance_id", referencedColumn: "instance_id", sameSchema: true}},
		}},
	},
	{
		name: pluginUpgradeJournalTableName,
		ddl: `CREATE TABLE IF NOT EXISTS plugin_upgrade_journal (
  journal_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  instance_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  from_version VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  to_version VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  error_code VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  error_message VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  started_at_ms BIGINT UNSIGNED NOT NULL,
  finished_at_ms BIGINT UNSIGNED NULL,
  PRIMARY KEY (journal_id),
  KEY plugin_upgrade_journal_instance_started_idx (instance_id, started_at_ms, journal_id),
  CONSTRAINT plugin_upgrade_journal_instance_fk FOREIGN KEY (instance_id)
    REFERENCES plugin_instances (instance_id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT plugin_upgrade_journal_invariants_check CHECK (
    CHAR_LENGTH(journal_id) BETWEEN 1 AND 128 AND journal_id = TRIM(journal_id) AND
    CHAR_LENGTH(instance_id) BETWEEN 1 AND 128 AND instance_id = TRIM(instance_id) AND
    CHAR_LENGTH(from_version) BETWEEN 1 AND 64 AND from_version = TRIM(from_version) AND
    CHAR_LENGTH(to_version) BETWEEN 1 AND 64 AND to_version = TRIM(to_version) AND
    status IN ('started', 'succeeded', 'failed', 'rolled_back') AND
    ((status = 'started') = (finished_at_ms IS NULL AND error_code IS NULL AND error_message IS NULL)) AND
    ((status IN ('failed', 'rolled_back')) = (error_code IS NOT NULL AND error_message IS NOT NULL)) AND
    (status = 'succeeded' OR status = 'started' OR error_code IS NOT NULL) AND
    (error_code IS NULL OR (CHAR_LENGTH(error_code) BETWEEN 1 AND 32 AND error_code = TRIM(error_code))) AND
    (error_message IS NULL OR (CHAR_LENGTH(error_message) BETWEEN 1 AND 512 AND error_message = TRIM(error_message))) AND
    started_at_ms > 0 AND
    (finished_at_ms IS NULL OR finished_at_ms >= started_at_ms)
  )
)`,
		columns: []schemaColumn{
			{name: "journal_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "instance_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "from_version", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "to_version", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "error_code", columnType: "varchar(32)", nullable: true, collation: "ascii_bin"},
			{name: "error_message", columnType: "varchar(512)", nullable: true, collation: "utf8mb4_bin"},
			{name: "started_at_ms", columnType: "bigint unsigned"},
			{name: "finished_at_ms", columnType: "bigint unsigned", nullable: true},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "journal_id"),
			ascendingBTreeIndex("plugin_upgrade_journal_instance_started_idx", false, "instance_id", "started_at_ms", "journal_id"),
		},
		checks: []schemaCheck{{
			name: "plugin_upgrade_journal_invariants_check",
			clause: "(((CHAR_LENGTH(`journal_id`) >= 1) and (CHAR_LENGTH(`journal_id`) <= 128)) and (`journal_id` = trim(`journal_id`)) and " +
				"((CHAR_LENGTH(`instance_id`) >= 1) and (CHAR_LENGTH(`instance_id`) <= 128)) and (`instance_id` = trim(`instance_id`)) and " +
				"((CHAR_LENGTH(`from_version`) >= 1) and (CHAR_LENGTH(`from_version`) <= 64)) and (`from_version` = trim(`from_version`)) and " +
				"((CHAR_LENGTH(`to_version`) >= 1) and (CHAR_LENGTH(`to_version`) <= 64)) and (`to_version` = trim(`to_version`)) and " +
				"(`status` in ('started','succeeded','failed','rolled_back')) and " +
				"((`status` = 'started') = ((`finished_at_ms` is null) and (`error_code` is null) and (`error_message` is null))) and " +
				"((`status` in ('failed','rolled_back')) = ((`error_code` is not null) and (`error_message` is not null))) and " +
				"((`status` = 'succeeded') or (`status` = 'started') or (`error_code` is not null)) and " +
				"((`error_code` is null) or (((CHAR_LENGTH(`error_code`) >= 1) and (CHAR_LENGTH(`error_code`) <= 32)) and (`error_code` = trim(`error_code`)))) and " +
				"((`error_message` is null) or (((CHAR_LENGTH(`error_message`) >= 1) and (CHAR_LENGTH(`error_message`) <= 512)) and (`error_message` = trim(`error_message`)))) and " +
				"(`started_at_ms` > 0) and ((`finished_at_ms` is null) or (`finished_at_ms` >= `started_at_ms`)))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "plugin_upgrade_journal_instance_fk",
			referencedTable: "plugin_instances",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns:         []schemaForeignKeyColumn{{name: "instance_id", referencedColumn: "instance_id", sameSchema: true}},
		}},
	},
	{
		name: pluginInstanceConfigRefsTableName,
		ddl: `CREATE TABLE IF NOT EXISTS plugin_instance_config_refs (
  instance_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  handle VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  secret_namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  secret_name VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (instance_id, handle),
  KEY plugin_instance_config_refs_secret_idx (secret_namespace, secret_name),
  CONSTRAINT plugin_instance_config_refs_instance_fk FOREIGN KEY (instance_id)
    REFERENCES plugin_instances (instance_id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT plugin_instance_config_refs_secret_fk FOREIGN KEY (secret_namespace, secret_name)
    REFERENCES secret_values (namespace, name) ON UPDATE RESTRICT ON DELETE RESTRICT,
  CONSTRAINT plugin_instance_config_refs_invariants_check CHECK (
    CHAR_LENGTH(instance_id) BETWEEN 1 AND 128 AND instance_id = TRIM(instance_id) AND
    CHAR_LENGTH(handle) BETWEEN 1 AND 128 AND handle = TRIM(handle) AND handle REGEXP '^[a-z0-9][a-z0-9._-]{0,127}$' AND
    CHAR_LENGTH(secret_namespace) BETWEEN 1 AND 64 AND secret_namespace = TRIM(secret_namespace) AND
    CHAR_LENGTH(secret_name) BETWEEN 1 AND 128 AND secret_name = TRIM(secret_name) AND
    created_at_ms > 0
  )
)`,
		columns: []schemaColumn{
			{name: "instance_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "handle", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "secret_namespace", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "secret_name", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "instance_id", "handle"),
			ascendingBTreeIndex("plugin_instance_config_refs_secret_idx", false, "secret_namespace", "secret_name"),
		},
		checks: []schemaCheck{{
			name: "plugin_instance_config_refs_invariants_check",
			clause: "(((CHAR_LENGTH(`instance_id`) >= 1) and (CHAR_LENGTH(`instance_id`) <= 128)) and (`instance_id` = trim(`instance_id`)) and " +
				"((CHAR_LENGTH(`handle`) >= 1) and (CHAR_LENGTH(`handle`) <= 128)) and (`handle` = trim(`handle`)) and (`handle` regexp '^[a-z0-9][a-z0-9._-]{0,127}$') and " +
				"((CHAR_LENGTH(`secret_namespace`) >= 1) and (CHAR_LENGTH(`secret_namespace`) <= 64)) and (`secret_namespace` = trim(`secret_namespace`)) and " +
				"((CHAR_LENGTH(`secret_name`) >= 1) and (CHAR_LENGTH(`secret_name`) <= 128)) and (`secret_name` = trim(`secret_name`)) and " +
				"(`created_at_ms` > 0))",
		}},
		foreignKeys: []schemaForeignKey{
			{
				name:            "plugin_instance_config_refs_instance_fk",
				referencedTable: "plugin_instances",
				updateRule:      "restrict",
				deleteRule:      "cascade",
				columns:         []schemaForeignKeyColumn{{name: "instance_id", referencedColumn: "instance_id", sameSchema: true}},
			},
			{
				name:            "plugin_instance_config_refs_secret_fk",
				referencedTable: "secret_values",
				updateRule:      "restrict",
				deleteRule:      "restrict",
				columns: []schemaForeignKeyColumn{
					{name: "secret_namespace", referencedColumn: "namespace", sameSchema: true},
					{name: "secret_name", referencedColumn: "name", sameSchema: true},
				},
			},
		},
	},
}

func pluginPersistenceSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(pluginPersistenceSchema))
	for _, table := range pluginPersistenceSchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func applyPluginPersistenceSchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range pluginPersistenceSchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB %s table: %w", table.name, err)
		}
	}
	return nil
}

func verifyPluginPersistenceSchema(ctx context.Context, connection *sql.Conn) error {
	if err := verifyObservabilityHistorySchema(ctx, connection); err != nil {
		return err
	}
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB plugin persistence CHECK enforcement metadata: %w", err)
	}
	for _, table := range pluginPersistenceSchema {
		if err := verifySchemaTable(ctx, connection, table, enforcedAvailable); err != nil {
			return err
		}
	}
	return nil
}
