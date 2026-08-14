package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const observabilityRecordsTableName = "observability_records"

// observabilityHistorySchema is immutable revision 12. It stores already
// redacted log, terminal trace, and metric-history projections as JSON
// objects. Raw messages, prompts, credentials, and capture bytes stay out.
var observabilityHistorySchema = [...]schemaTable{
	{
		name: observabilityRecordsTableName,
		ddl: `CREATE TABLE IF NOT EXISTS observability_records (
  kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  record_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  recorded_at_ms BIGINT UNSIGNED NOT NULL,
  payload JSON NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (kind, record_key),
  KEY observability_records_kind_recorded_idx (kind, recorded_at_ms, record_key),
  CONSTRAINT observability_records_invariants_check CHECK (
    kind IN ('log', 'trace', 'metric') AND
    CHAR_LENGTH(record_key) BETWEEN 1 AND 128 AND record_key = TRIM(record_key) AND
    recorded_at_ms > 0 AND
    created_at_ms > 0 AND
    JSON_TYPE(payload) = 'OBJECT'
  )
)`,
		columns: []schemaColumn{
			{name: "kind", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "record_key", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "recorded_at_ms", columnType: "bigint unsigned"},
			{name: "payload", columnType: "json"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "kind", "record_key"),
			ascendingBTreeIndex("observability_records_kind_recorded_idx", false,
				"kind", "recorded_at_ms", "record_key"),
		},
		checks: []schemaCheck{{
			name: "observability_records_invariants_check",
			clause: "((`kind` in ('log','trace','metric')) and " +
				"((CHAR_LENGTH(`record_key`) >= 1) and (CHAR_LENGTH(`record_key`) <= 128)) and (`record_key` = trim(`record_key`)) and " +
				"(`recorded_at_ms` > 0) and (`created_at_ms` > 0) and (JSON_TYPE(`payload`) = 'OBJECT'))",
		}},
	},
}

func observabilityHistorySchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(observabilityHistorySchema))
	for _, table := range observabilityHistorySchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func applyObservabilityHistorySchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range observabilityHistorySchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB %s table: %w", table.name, err)
		}
	}
	return nil
}

func verifyObservabilityHistorySchema(ctx context.Context, connection *sql.Conn) error {
	if err := verifyToolExecutionLedgerSchema(ctx, connection); err != nil {
		return err
	}
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB observability history CHECK enforcement metadata: %w", err)
	}
	for _, table := range observabilityHistorySchema {
		if err := verifySchemaTable(ctx, connection, table, enforcedAvailable); err != nil {
			return err
		}
	}
	return nil
}
