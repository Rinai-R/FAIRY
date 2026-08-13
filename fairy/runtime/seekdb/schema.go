package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const foundationSchemaRevision int64 = 1

// BuiltinMigrations returns FAIRY's immutable, ordered SeekDB schema chain.
// Callers receive a new slice so changing the returned value cannot alter the
// process-wide schema definition.
func BuiltinMigrations() []Migration {
	migration := Migration{
		Revision: CurrentSchemaRevision(),
		Name:     "create-foundation-schema",
		Apply:    applyFoundationSchema,
		Verify:   verifyFoundationSchema,
	}
	return []Migration{migration}
}

// CurrentSchemaRevision is the exact revision accepted by runtime readiness.
func CurrentSchemaRevision() Revision {
	return Revision{
		Number:   foundationSchemaRevision,
		Checksum: foundationSchemaChecksum(),
	}
}

type schemaTable struct {
	name        string
	ddl         string
	columns     []schemaColumn
	indexes     []schemaIndex
	checks      []schemaCheck
	foreignKeys []schemaForeignKey
}

type schemaColumn struct {
	name                 string
	columnType           string
	nullable             bool
	collation            string
	defaultValue         sql.NullString
	extra                string
	generationExpression string
}

type schemaIndex struct {
	name      string
	unique    bool
	indexType string
	columns   []schemaIndexColumn
}

type schemaIndexColumn struct {
	name      string
	subPart   sql.NullInt64
	collation sql.NullString
}

type schemaCheck struct {
	name   string
	clause string
}

type schemaForeignKey struct {
	name            string
	referencedTable string
	updateRule      string
	deleteRule      string
	columns         []schemaForeignKeyColumn
}

type schemaForeignKeyColumn struct {
	name             string
	referencedColumn string
	sameSchema       bool
}

func ascendingBTreeIndex(name string, unique bool, columns ...string) schemaIndex {
	indexColumns := make([]schemaIndexColumn, len(columns))
	for index, column := range columns {
		indexColumns[index] = schemaIndexColumn{
			name:      column,
			collation: sql.NullString{String: "a", Valid: true},
		}
	}
	return schemaIndex{name: name, unique: unique, indexType: "btree", columns: indexColumns}
}

var integerDisplayWidthPattern = regexp.MustCompile(`^(tinyint|smallint|mediumint|int|bigint)\([0-9]+\)( unsigned)?$`)

var foundationSchema = [...]schemaTable{
	{
		name: "config_documents",
		ddl: `CREATE TABLE IF NOT EXISTS config_documents (
  namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  document_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  schema_version BIGINT UNSIGNED NOT NULL,
  revision BIGINT UNSIGNED NOT NULL,
  document JSON NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (namespace, document_key),
  KEY config_documents_updated_idx (namespace, updated_at_ms),
  CONSTRAINT config_documents_invariants_check CHECK (
    schema_version > 0 AND
    revision > 0 AND
    JSON_TYPE(document) = 'OBJECT' AND
    updated_at_ms >= created_at_ms
  )
)`,
		columns: []schemaColumn{
			{name: "namespace", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "document_key", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "schema_version", columnType: "bigint unsigned"},
			{name: "revision", columnType: "bigint unsigned"},
			{name: "document", columnType: "json"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "namespace", "document_key"),
			ascendingBTreeIndex("config_documents_updated_idx", false, "namespace", "updated_at_ms"),
		},
		checks: []schemaCheck{{
			name:   "config_documents_invariants_check",
			clause: "((`schema_version` > 0) and (`revision` > 0) and (JSON_TYPE(`document`) = 'OBJECT') and (`updated_at_ms` >= `created_at_ms`))",
		}},
	},
	{
		name: "secret_values",
		ddl: `CREATE TABLE IF NOT EXISTS secret_values (
  namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  name VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  key_version BIGINT UNSIGNED NOT NULL,
  nonce VARBINARY(12) NOT NULL,
  ciphertext LONGBLOB NOT NULL,
  aad VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (namespace, name),
  KEY secret_values_updated_idx (namespace, updated_at_ms),
  CONSTRAINT secret_values_invariants_check CHECK (
    key_version > 0 AND
    OCTET_LENGTH(nonce) = 12 AND
    OCTET_LENGTH(ciphertext) > 0 AND
    OCTET_LENGTH(aad) > 0 AND
    updated_at_ms >= created_at_ms
  )
)`,
		columns: []schemaColumn{
			{name: "namespace", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "name", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "key_version", columnType: "bigint unsigned"},
			{name: "nonce", columnType: "varbinary(12)"},
			{name: "ciphertext", columnType: "longblob"},
			{name: "aad", columnType: "varchar(512)", collation: "utf8mb4_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "namespace", "name"),
			ascendingBTreeIndex("secret_values_updated_idx", false, "namespace", "updated_at_ms"),
		},
		checks: []schemaCheck{{
			name:   "secret_values_invariants_check",
			clause: "((`key_version` > 0) and (OCTET_LENGTH(`nonce`) = 12) and (OCTET_LENGTH(`ciphertext`) > 0) and (OCTET_LENGTH(`aad`) > 0) and (`updated_at_ms` >= `created_at_ms`))",
		}},
	},
	{
		name: "owner_identities",
		ddl: `CREATE TABLE IF NOT EXISTS owner_identities (
  namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  subject_digest BINARY(32) NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (namespace, subject_digest),
  KEY owner_identities_created_idx (created_at_ms),
  CONSTRAINT owner_identities_invariants_check CHECK (created_at_ms > 0)
)`,
		columns: []schemaColumn{
			{name: "namespace", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "subject_digest", columnType: "binary(32)"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "namespace", "subject_digest"),
			ascendingBTreeIndex("owner_identities_created_idx", false, "created_at_ms"),
		},
		checks: []schemaCheck{{
			name:   "owner_identities_invariants_check",
			clause: "(`created_at_ms` > 0)",
		}},
	},
	{
		name: "characters",
		ddl: `CREATE TABLE IF NOT EXISTS characters (
  character_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  revision BIGINT UNSIGNED NOT NULL,
  name VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  snapshot JSON NOT NULL,
  appearance_ref VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (character_id),
  KEY characters_updated_idx (updated_at_ms),
  CONSTRAINT characters_invariants_check CHECK (
    revision > 0 AND
    CHAR_LENGTH(name) > 0 AND
    name = TRIM(name) AND
    JSON_TYPE(snapshot) = 'OBJECT' AND
    (appearance_ref IS NULL OR (CHAR_LENGTH(appearance_ref) > 0 AND appearance_ref = TRIM(appearance_ref))) AND
    updated_at_ms >= created_at_ms
  )
)`,
		columns: []schemaColumn{
			{name: "character_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "revision", columnType: "bigint unsigned"},
			{name: "name", columnType: "varchar(200)", collation: "utf8mb4_bin"},
			{name: "snapshot", columnType: "json"},
			{name: "appearance_ref", columnType: "varchar(255)", nullable: true, collation: "utf8mb4_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "character_id"),
			ascendingBTreeIndex("characters_updated_idx", false, "updated_at_ms"),
		},
		checks: []schemaCheck{{
			name:   "characters_invariants_check",
			clause: "((`revision` > 0) and (CHAR_LENGTH(`name`) > 0) and (`name` = trim(`name`)) and (JSON_TYPE(`snapshot`) = 'OBJECT') and ((`appearance_ref` is null) or ((CHAR_LENGTH(`appearance_ref`) > 0) and (`appearance_ref` = trim(`appearance_ref`)))) and (`updated_at_ms` >= `created_at_ms`))",
		}},
	},
	{
		name: "plugin_packages",
		ddl: `CREATE TABLE IF NOT EXISTS plugin_packages (
  plugin_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  version VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  abi_version BIGINT UNSIGNED NOT NULL,
  artifact_sha256 BINARY(32) NOT NULL,
  publisher_digest BINARY(32) NULL,
  manifest JSON NOT NULL,
  verified_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (plugin_id, version),
  UNIQUE KEY plugin_packages_artifact_key (artifact_sha256),
  KEY plugin_packages_verified_idx (verified_at_ms),
  CONSTRAINT plugin_packages_invariants_check CHECK (
    abi_version > 0 AND
    JSON_TYPE(manifest) = 'OBJECT' AND
    verified_at_ms > 0
  )
)`,
		columns: []schemaColumn{
			{name: "plugin_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "version", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "abi_version", columnType: "bigint unsigned"},
			{name: "artifact_sha256", columnType: "binary(32)"},
			{name: "publisher_digest", columnType: "binary(32)", nullable: true},
			{name: "manifest", columnType: "json"},
			{name: "verified_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "plugin_id", "version"),
			ascendingBTreeIndex("plugin_packages_artifact_key", true, "artifact_sha256"),
			ascendingBTreeIndex("plugin_packages_verified_idx", false, "verified_at_ms"),
		},
		checks: []schemaCheck{{
			name:   "plugin_packages_invariants_check",
			clause: "((`abi_version` > 0) and (JSON_TYPE(`manifest`) = 'OBJECT') and (`verified_at_ms` > 0))",
		}},
	},
	{
		name: "plugin_instances",
		ddl: `CREATE TABLE IF NOT EXISTS plugin_instances (
  instance_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  plugin_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  plugin_version VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  enabled TINYINT UNSIGNED NOT NULL,
  lifecycle_state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  capability_grants JSON NOT NULL,
  config_document JSON NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (instance_id),
  KEY plugin_instances_package_idx (plugin_id, plugin_version),
  KEY plugin_instances_state_updated_idx (lifecycle_state, updated_at_ms),
  CONSTRAINT plugin_instances_package_fk FOREIGN KEY (plugin_id, plugin_version)
    REFERENCES plugin_packages (plugin_id, version) ON UPDATE RESTRICT ON DELETE RESTRICT,
  CONSTRAINT plugin_instances_invariants_check CHECK (
    enabled IN (0, 1) AND
    lifecycle_state IN ('disabled', 'ready', 'degraded', 'failed') AND
    ((enabled = 0 AND lifecycle_state = 'disabled') OR
      (enabled = 1 AND lifecycle_state IN ('ready', 'degraded', 'failed'))) AND
    JSON_TYPE(capability_grants) = 'ARRAY' AND
    JSON_TYPE(config_document) = 'OBJECT' AND
    updated_at_ms >= created_at_ms
  )
)`,
		columns: []schemaColumn{
			{name: "instance_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "plugin_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "plugin_version", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "enabled", columnType: "tinyint unsigned"},
			{name: "lifecycle_state", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "capability_grants", columnType: "json"},
			{name: "config_document", columnType: "json"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "instance_id"),
			ascendingBTreeIndex("plugin_instances_package_idx", false, "plugin_id", "plugin_version"),
			ascendingBTreeIndex("plugin_instances_state_updated_idx", false, "lifecycle_state", "updated_at_ms"),
		},
		checks: []schemaCheck{{
			name:   "plugin_instances_invariants_check",
			clause: "((`enabled` in (0,1)) and (`lifecycle_state` in ('disabled','ready','degraded','failed')) and (((`enabled` = 0) and (`lifecycle_state` = 'disabled')) or ((`enabled` = 1) and (`lifecycle_state` in ('ready','degraded','failed')))) and (JSON_TYPE(`capability_grants`) = 'ARRAY') and (JSON_TYPE(`config_document`) = 'OBJECT') and (`updated_at_ms` >= `created_at_ms`))",
		}},
		foreignKeys: []schemaForeignKey{
			{
				name:            "plugin_instances_package_fk",
				referencedTable: "plugin_packages",
				updateRule:      "restrict",
				deleteRule:      "restrict",
				columns: []schemaForeignKeyColumn{
					{name: "plugin_id", referencedColumn: "plugin_id", sameSchema: true},
					{name: "plugin_version", referencedColumn: "version", sameSchema: true},
				},
			},
		},
	},
}

func foundationSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(foundationSchema))
	for _, table := range foundationSchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func schemaDDLChecksum(statements []string) [sha256.Size]byte {
	normalized := make([]string, len(statements))
	for index, statement := range statements {
		normalized[index] = normalizeDDL(statement)
	}
	return sha256.Sum256([]byte(strings.Join(normalized, "\n;\n")))
}

func normalizeDDL(statement string) string {
	var normalized strings.Builder
	normalized.Grow(len(statement))
	var quote byte
	pendingWhitespace := false
	for index := 0; index < len(statement); index++ {
		current := statement[index]
		if quote != 0 {
			normalized.WriteByte(current)
			if current == '\\' && index+1 < len(statement) {
				index++
				normalized.WriteByte(statement[index])
				continue
			}
			if current != quote {
				continue
			}
			if index+1 < len(statement) && statement[index+1] == quote {
				index++
				normalized.WriteByte(statement[index])
				continue
			}
			quote = 0
			continue
		}
		if isSQLWhitespace(current) {
			pendingWhitespace = normalized.Len() > 0
			continue
		}
		if pendingWhitespace {
			normalized.WriteByte(' ')
			pendingWhitespace = false
		}
		if current == '\'' || current == '"' || current == '`' {
			quote = current
		}
		normalized.WriteByte(current)
	}
	return normalized.String()
}

func isSQLWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}

func applyFoundationSchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range foundationSchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB foundation table %s: %w", table.name, err)
		}
	}
	return nil
}

func verifyFoundationSchema(ctx context.Context, connection *sql.Conn) error {
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB CHECK enforcement metadata: %w", err)
	}
	for _, table := range foundationSchema {
		if err := verifySchemaTable(ctx, connection, table, enforcedAvailable); err != nil {
			return err
		}
	}
	return nil
}

func verifySchemaTable(ctx context.Context, connection *sql.Conn, expected schemaTable, checkEnforcementAvailable bool) error {
	columns, err := readSchemaColumns(ctx, connection, expected.name)
	if err != nil {
		return fmt.Errorf("verify SeekDB table %s columns: %w", expected.name, err)
	}
	if err := compareSchemaColumns(expected.columns, columns); err != nil {
		return fmt.Errorf("verify SeekDB table %s columns: %w", expected.name, err)
	}
	indexes, err := readSchemaIndexes(ctx, connection, expected.name)
	if err != nil {
		return fmt.Errorf("verify SeekDB table %s indexes: %w", expected.name, err)
	}
	if err := compareSchemaIndexes(expected.indexes, indexes); err != nil {
		return fmt.Errorf("verify SeekDB table %s indexes: %w", expected.name, err)
	}
	checks, err := readSchemaChecks(ctx, connection, expected.name, checkEnforcementAvailable)
	if err != nil {
		return fmt.Errorf("verify SeekDB table %s checks: %w", expected.name, err)
	}
	if err := compareSchemaChecks(expected.checks, checks); err != nil {
		return fmt.Errorf("verify SeekDB table %s checks: %w", expected.name, err)
	}
	foreignKeys, err := readSchemaForeignKeys(ctx, connection, expected.name)
	if err != nil {
		return fmt.Errorf("verify SeekDB table %s foreign keys: %w", expected.name, err)
	}
	if err := compareSchemaForeignKeys(expected.foreignKeys, foreignKeys); err != nil {
		return fmt.Errorf("verify SeekDB table %s foreign keys: %w", expected.name, err)
	}
	return nil
}

func readSchemaColumns(ctx context.Context, connection *sql.Conn, table string) ([]schemaColumn, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT column_name, column_type, is_nullable, COALESCE(collation_name, ''),
       column_default, COALESCE(extra, ''), COALESCE(generation_expression, '')
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ?
ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []schemaColumn
	for rows.Next() {
		var column schemaColumn
		var nullable string
		if err := rows.Scan(
			&column.name,
			&column.columnType,
			&nullable,
			&column.collation,
			&column.defaultValue,
			&column.extra,
			&column.generationExpression,
		); err != nil {
			return nil, err
		}
		column.columnType = canonicalSchemaColumnType(column.columnType)
		column.collation = strings.ToLower(column.collation)
		column.extra = strings.ToLower(normalizeDDL(column.extra))
		column.generationExpression = normalizeDDL(column.generationExpression)
		switch nullable {
		case "YES":
			column.nullable = true
		case "NO":
		default:
			return nil, fmt.Errorf("column %s has unknown nullable value %q", column.name, nullable)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func canonicalSchemaColumnType(raw string) string {
	columnType := strings.ToLower(raw)
	return integerDisplayWidthPattern.ReplaceAllString(columnType, `$1$2`)
}

func compareSchemaColumns(expected, actual []schemaColumn) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("column count = %d, want %d", len(actual), len(expected))
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return fmt.Errorf("column %d = %#v, want %#v", index+1, actual[index], expected[index])
		}
	}
	return nil
}

func readSchemaIndexes(ctx context.Context, connection *sql.Conn, table string) ([]schemaIndex, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT index_name, non_unique, seq_in_index, column_name, sub_part, collation, index_type
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ?
ORDER BY index_name, seq_in_index`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var indexes []schemaIndex
	for rows.Next() {
		var name, column, indexType string
		var subPart sql.NullInt64
		var collation sql.NullString
		var nonUnique, sequence int
		if err := rows.Scan(&name, &nonUnique, &sequence, &column, &subPart, &collation, &indexType); err != nil {
			return nil, err
		}
		if nonUnique != 0 && nonUnique != 1 {
			return nil, fmt.Errorf("index %s has invalid non_unique value %d", name, nonUnique)
		}
		if sequence <= 0 {
			return nil, fmt.Errorf("index %s has invalid sequence %d", name, sequence)
		}
		if len(indexes) == 0 || indexes[len(indexes)-1].name != name {
			indexes = append(indexes, schemaIndex{
				name:      name,
				unique:    nonUnique == 0,
				indexType: strings.ToLower(indexType),
			})
		}
		current := &indexes[len(indexes)-1]
		if current.unique != (nonUnique == 0) || current.indexType != strings.ToLower(indexType) || sequence != len(current.columns)+1 {
			return nil, fmt.Errorf("index %s metadata is inconsistent", name)
		}
		if collation.Valid {
			collation.String = strings.ToLower(collation.String)
		}
		current.columns = append(current.columns, schemaIndexColumn{
			name:      column,
			subPart:   subPart,
			collation: collation,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return indexes, nil
}

func compareSchemaIndexes(expected, actual []schemaIndex) error {
	want := slices.Clone(expected)
	got := slices.Clone(actual)
	compareName := func(left, right schemaIndex) int {
		return strings.Compare(strings.ToLower(left.name), strings.ToLower(right.name))
	}
	slices.SortFunc(want, compareName)
	slices.SortFunc(got, compareName)
	if len(got) != len(want) {
		return fmt.Errorf("index count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].name != want[index].name ||
			got[index].unique != want[index].unique ||
			got[index].indexType != want[index].indexType ||
			!slices.Equal(got[index].columns, want[index].columns) {
			return fmt.Errorf("index %d = %#v, want %#v", index+1, got[index], want[index])
		}
	}
	return nil
}

func readSchemaChecks(ctx context.Context, connection *sql.Conn, table string, enforcedAvailable bool) ([]schemaCheck, error) {
	enforcedSelection := "'YES'"
	if enforcedAvailable {
		enforcedSelection = "tc.enforced"
	}
	rows, err := connection.QueryContext(ctx, `
SELECT tc.constraint_name, cc.check_clause, `+enforcedSelection+`
FROM information_schema.table_constraints tc
JOIN information_schema.check_constraints cc
  ON cc.constraint_schema = tc.constraint_schema
 AND cc.constraint_name = tc.constraint_name
WHERE tc.table_schema = DATABASE() AND tc.table_name = ? AND tc.constraint_type = 'CHECK'
ORDER BY tc.constraint_name`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var checks []schemaCheck
	for rows.Next() {
		var check schemaCheck
		var enforced string
		if err := rows.Scan(&check.name, &check.clause, &enforced); err != nil {
			return nil, err
		}
		if enforcedAvailable && !strings.EqualFold(enforced, "YES") {
			return nil, fmt.Errorf("check constraint %s is not enforced", check.name)
		}
		check.clause = normalizeDDL(check.clause)
		checks = append(checks, check)
	}
	return checks, errors.Join(rows.Err())
}

func schemaCheckEnforcementAvailable(ctx context.Context, connection *sql.Conn) (bool, error) {
	var count int
	if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = 'information_schema'
  AND table_name = 'TABLE_CONSTRAINTS'
  AND column_name = 'ENFORCED'`).Scan(&count); err != nil {
		return false, err
	}
	switch count {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("information_schema.table_constraints ENFORCED column count = %d, want at most 1", count)
	}
}

func compareSchemaChecks(expected, actual []schemaCheck) error {
	want := slices.Clone(expected)
	got := slices.Clone(actual)
	compareName := func(left, right schemaCheck) int {
		return strings.Compare(strings.ToLower(left.name), strings.ToLower(right.name))
	}
	slices.SortFunc(want, compareName)
	slices.SortFunc(got, compareName)
	if len(got) != len(want) {
		return fmt.Errorf("check constraint count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		want[index].clause = normalizeDDL(want[index].clause)
		got[index].clause = normalizeDDL(got[index].clause)
		if got[index] != want[index] {
			return fmt.Errorf("check constraint %d = %#v, want %#v", index+1, got[index], want[index])
		}
	}
	return nil
}

func readSchemaForeignKeys(ctx context.Context, connection *sql.Conn, table string) ([]schemaForeignKey, error) {
	constraintNames, err := readSchemaForeignKeyNames(ctx, connection, table)
	if err != nil {
		return nil, err
	}
	// SeekDB v1.3.0 can invalidate the session for a KEY_COLUMN_USAGE /
	// REFERENTIAL_CONSTRAINTS join. TABLE_CONSTRAINTS is the authoritative,
	// safe presence probe; read columns and rules with separate queries and
	// merge them only after both result sets have been closed.
	if len(constraintNames) == 0 {
		return nil, nil
	}
	foreignKeys, err := readSchemaForeignKeyColumns(ctx, connection, table)
	if err != nil {
		return nil, err
	}
	rules, err := readSchemaForeignKeyRules(ctx, connection, table)
	if err != nil {
		return nil, err
	}
	observedNames := make([]string, len(foreignKeys))
	for index := range foreignKeys {
		foreignKey := &foreignKeys[index]
		observedNames[index] = foreignKey.name
		rule, ok := rules[foreignKey.name]
		if !ok {
			return nil, fmt.Errorf("foreign key %s has no referential rule metadata", foreignKey.name)
		}
		foreignKey.updateRule = rule.update
		foreignKey.deleteRule = rule.delete
		delete(rules, foreignKey.name)
	}
	if len(rules) != 0 {
		unexpected := make([]string, 0, len(rules))
		for name := range rules {
			unexpected = append(unexpected, name)
		}
		slices.Sort(unexpected)
		return nil, fmt.Errorf("referential rules without foreign key columns: %v", unexpected)
	}
	slices.Sort(constraintNames)
	slices.Sort(observedNames)
	if !slices.Equal(observedNames, constraintNames) {
		return nil, fmt.Errorf("foreign key detail names = %v, want %v", observedNames, constraintNames)
	}
	return foreignKeys, nil
}

func readSchemaForeignKeyColumns(ctx context.Context, connection *sql.Conn, table string) ([]schemaForeignKey, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT k.constraint_name, k.ordinal_position, k.column_name,
       k.referenced_table_name, k.referenced_column_name,
       k.referenced_table_schema = DATABASE()
FROM information_schema.key_column_usage k
WHERE k.table_schema = DATABASE()
  AND k.table_name = ?
  AND k.referenced_table_name IS NOT NULL
ORDER BY k.constraint_name, k.ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var foreignKeys []schemaForeignKey
	for rows.Next() {
		var (
			name, column, referencedTable, referencedColumn string
			ordinal, sameSchema                             int
		)
		if err := rows.Scan(
			&name,
			&ordinal,
			&column,
			&referencedTable,
			&referencedColumn,
			&sameSchema,
		); err != nil {
			return nil, err
		}
		if ordinal <= 0 || (sameSchema != 0 && sameSchema != 1) {
			return nil, fmt.Errorf("foreign key %s metadata is invalid", name)
		}
		if len(foreignKeys) == 0 || foreignKeys[len(foreignKeys)-1].name != name {
			foreignKeys = append(foreignKeys, schemaForeignKey{
				name:            name,
				referencedTable: referencedTable,
			})
		}
		current := &foreignKeys[len(foreignKeys)-1]
		if ordinal != len(current.columns)+1 ||
			current.referencedTable != referencedTable {
			return nil, fmt.Errorf("foreign key %s metadata is inconsistent", name)
		}
		current.columns = append(current.columns, schemaForeignKeyColumn{
			name:             column,
			referencedColumn: referencedColumn,
			sameSchema:       sameSchema == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return foreignKeys, nil
}

type schemaForeignKeyRule struct {
	update string
	delete string
}

func readSchemaForeignKeyRules(ctx context.Context, connection *sql.Conn, table string) (map[string]schemaForeignKeyRule, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT constraint_name, update_rule, delete_rule
FROM information_schema.referential_constraints
WHERE constraint_schema = DATABASE()
  AND table_name = ?
ORDER BY constraint_name`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make(map[string]schemaForeignKeyRule)
	for rows.Next() {
		var name, updateRule, deleteRule string
		if err := rows.Scan(&name, &updateRule, &deleteRule); err != nil {
			return nil, err
		}
		if _, duplicate := rules[name]; duplicate {
			return nil, fmt.Errorf("foreign key %s has duplicate referential rule metadata", name)
		}
		rules[name] = schemaForeignKeyRule{
			update: strings.ToLower(updateRule),
			delete: strings.ToLower(deleteRule),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}

func readSchemaForeignKeyNames(ctx context.Context, connection *sql.Conn, table string) ([]string, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT constraint_name
FROM information_schema.table_constraints
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND constraint_type = 'FOREIGN KEY'
ORDER BY constraint_name`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, errors.Join(rows.Err())
}

func compareSchemaForeignKeys(expected, actual []schemaForeignKey) error {
	want := slices.Clone(expected)
	got := slices.Clone(actual)
	compareName := func(left, right schemaForeignKey) int {
		return strings.Compare(strings.ToLower(left.name), strings.ToLower(right.name))
	}
	slices.SortFunc(want, compareName)
	slices.SortFunc(got, compareName)
	if len(got) != len(want) {
		return fmt.Errorf("foreign key count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].name != want[index].name ||
			got[index].referencedTable != want[index].referencedTable ||
			got[index].updateRule != want[index].updateRule ||
			got[index].deleteRule != want[index].deleteRule ||
			!slices.Equal(got[index].columns, want[index].columns) {
			return fmt.Errorf("foreign key %d = %#v, want %#v", index+1, got[index], want[index])
		}
	}
	return nil
}
