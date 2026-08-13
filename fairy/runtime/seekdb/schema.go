package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	foundationSchemaRevision          int64 = 1
	conversationSchemaRevision        int64 = 2
	turnEvidenceSchemaRevision        int64 = 3
	transcriptRecallSchemaRevision    int64 = 4
	conversationRuntimeSchemaRevision int64 = 5

	transcriptRecallTableName = "conversation_messages"
	transcriptRecallIndexName = "conversation_messages_content_fts_idx"
	transcriptRecallIndexDDL  = "CREATE FULLTEXT INDEX IF NOT EXISTS conversation_messages_content_fts_idx " +
		"ON conversation_messages(content) WITH PARSER IK PARSER_PROPERTIES=(ik_mode='max_word')"
)

// BuiltinMigrations returns FAIRY's immutable, ordered SeekDB schema chain.
// Callers receive a new slice so changing the returned value cannot alter the
// process-wide schema definition.
func BuiltinMigrations() []Migration {
	return []Migration{
		{
			Revision: Revision{
				Number:   foundationSchemaRevision,
				Checksum: foundationSchemaChecksum(),
			},
			Name:   "create-foundation-schema",
			Apply:  applyFoundationSchema,
			Verify: verifyFoundationSchema,
		},
		{
			Revision: Revision{
				Number:   conversationSchemaRevision,
				Checksum: conversationSchemaChecksum(),
			},
			Name:   "create-conversation-schema",
			Apply:  applyConversationSchema,
			Verify: verifyConversationSchema,
		},
		{
			Revision: Revision{
				Number:   turnEvidenceSchemaRevision,
				Checksum: turnEvidenceSchemaChecksum(),
			},
			Name:   "create-turn-evidence-schema",
			Apply:  applyTurnEvidenceSchema,
			Verify: verifyTurnEvidenceSchema,
		},
		{
			Revision: Revision{
				Number:   transcriptRecallSchemaRevision,
				Checksum: transcriptRecallSchemaChecksum(),
			},
			Name:   "create-conversation-message-fulltext-index",
			Apply:  applyTranscriptRecallSchema,
			Verify: verifyTranscriptRecallSchema,
		},
		{
			Revision: Revision{
				Number:   conversationRuntimeSchemaRevision,
				Checksum: conversationRuntimeSchemaChecksum(),
			},
			Name:   "create-conversation-runtime-schema",
			Apply:  applyConversationRuntimeSchema,
			Verify: verifyConversationRuntimeSchema,
		},
	}
}

// CurrentSchemaRevision is the exact revision accepted by runtime readiness.
func CurrentSchemaRevision() Revision {
	return Revision{
		Number:   conversationRuntimeSchemaRevision,
		Checksum: conversationRuntimeSchemaChecksum(),
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

type transcriptRecallIndexMetadata struct {
	table     string
	name      string
	nonUnique int
	sequence  int
	column    string
	collation sql.NullString
	subPart   sql.NullInt64
	indexType string
	comment   string
	visible   string
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

// conversationSchema is revision 2. It deliberately preserves the durable
// conversation identities used by the Session contract while strengthening
// three isolation boundaries that PostgreSQL previously left to application
// code: one authoritative default conversation exists per character, an
// endpoint binding must name a conversation owned by the same character, and
// a message must name a turn from the same conversation.
var conversationSchema = [...]schemaTable{
	{
		name: "conversations",
		ddl: `CREATE TABLE IF NOT EXISTS conversations (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  character_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY conversations_identity_character_kind_key (id, character_id, kind),
  KEY conversations_character_updated_idx (character_id, updated_at_ms, id),
  CONSTRAINT conversations_invariants_check CHECK (
    CHAR_LENGTH(id) > 0 AND id = TRIM(id) AND
    CHAR_LENGTH(character_id) > 0 AND character_id = TRIM(character_id) AND
    kind IN ('character', 'endpoint') AND
    updated_at_ms >= created_at_ms
  )
)`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "character_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "kind", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex("conversations_identity_character_kind_key", true, "id", "character_id", "kind"),
			ascendingBTreeIndex("conversations_character_updated_idx", false, "character_id", "updated_at_ms", "id"),
		},
		checks: []schemaCheck{{
			name:   "conversations_invariants_check",
			clause: "((CHAR_LENGTH(`id`) > 0) and (`id` = trim(`id`)) and (CHAR_LENGTH(`character_id`) > 0) and (`character_id` = trim(`character_id`)) and (`kind` in ('character','endpoint')) and (`updated_at_ms` >= `created_at_ms`))",
		}},
	},
	{
		name: "character_conversations",
		ddl: `CREATE TABLE IF NOT EXISTS character_conversations (
  character_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  PRIMARY KEY (character_id),
  UNIQUE KEY character_conversations_conversation_key (conversation_id),
  CONSTRAINT character_conversations_conversation_fk FOREIGN KEY (conversation_id, character_id, kind)
    REFERENCES conversations (id, character_id, kind) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT character_conversations_invariants_check CHECK (
    CHAR_LENGTH(character_id) > 0 AND character_id = TRIM(character_id) AND
    CHAR_LENGTH(conversation_id) > 0 AND conversation_id = TRIM(conversation_id) AND
    kind = 'character'
  )
)`,
		columns: []schemaColumn{
			{name: "character_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "kind", columnType: "varchar(16)", collation: "ascii_bin"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "character_id"),
			ascendingBTreeIndex("character_conversations_conversation_key", true, "conversation_id"),
		},
		checks: []schemaCheck{{
			name:   "character_conversations_invariants_check",
			clause: "((CHAR_LENGTH(`character_id`) > 0) and (`character_id` = trim(`character_id`)) and (CHAR_LENGTH(`conversation_id`) > 0) and (`conversation_id` = trim(`conversation_id`)) and (`kind` = 'character'))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "character_conversations_conversation_fk",
			referencedTable: "conversations",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "id", sameSchema: true},
				{name: "character_id", referencedColumn: "character_id", sameSchema: true},
				{name: "kind", referencedColumn: "kind", sameSchema: true},
			},
		}},
	},
	{
		name: "endpoint_conversations",
		ddl: `CREATE TABLE IF NOT EXISTS endpoint_conversations (
  character_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  endpoint VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  endpoint_key_digest BINARY(32) NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  audience VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  initiation VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  presentation VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  evaluation TINYINT UNSIGNED NOT NULL,
  principal_namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  principal_digest BINARY(32) NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (character_id, endpoint, endpoint_key_digest),
  UNIQUE KEY endpoint_conversations_conversation_key (conversation_id),
  CONSTRAINT endpoint_conversations_conversation_fk FOREIGN KEY (conversation_id, character_id, kind)
    REFERENCES conversations (id, character_id, kind) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT endpoint_conversations_invariants_check CHECK (
    endpoint IN ('desktop', 'im') AND
    audience IN ('single', 'multi') AND
    initiation IN ('direct', 'ambient') AND
    presentation IN ('embodied', 'chat') AND
    kind = 'endpoint' AND
    evaluation IN (0, 1) AND
    ((principal_namespace IS NULL) = (principal_digest IS NULL)) AND
    ((endpoint = 'im' AND audience = 'single') =
      (principal_namespace IS NOT NULL AND principal_digest IS NOT NULL)) AND
    (principal_namespace IS NULL OR
      (CHAR_LENGTH(principal_namespace) > 0 AND principal_namespace = TRIM(principal_namespace))) AND
    (evaluation = 0 OR
      (endpoint = 'desktop' AND audience = 'single' AND initiation = 'direct' AND presentation = 'chat')) AND
    updated_at_ms >= created_at_ms
  )
)`,
		columns: []schemaColumn{
			{name: "character_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "endpoint", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "endpoint_key_digest", columnType: "binary(32)"},
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "kind", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "audience", columnType: "varchar(8)", collation: "ascii_bin"},
			{name: "initiation", columnType: "varchar(8)", collation: "ascii_bin"},
			{name: "presentation", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "evaluation", columnType: "tinyint unsigned"},
			{name: "principal_namespace", columnType: "varchar(64)", nullable: true, collation: "ascii_bin"},
			{name: "principal_digest", columnType: "binary(32)", nullable: true},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "character_id", "endpoint", "endpoint_key_digest"),
			ascendingBTreeIndex("endpoint_conversations_conversation_key", true, "conversation_id"),
		},
		checks: []schemaCheck{{
			name: "endpoint_conversations_invariants_check",
			clause: "((`endpoint` in ('desktop','im')) and (`audience` in ('single','multi')) and " +
				"(`initiation` in ('direct','ambient')) and (`presentation` in ('embodied','chat')) and " +
				"(`kind` = 'endpoint') and " +
				"(`evaluation` in (0,1)) and ((`principal_namespace` is null) = (`principal_digest` is null)) and " +
				"(((`endpoint` = 'im') and (`audience` = 'single')) = ((`principal_namespace` is not null) and (`principal_digest` is not null))) and " +
				"((`principal_namespace` is null) or ((CHAR_LENGTH(`principal_namespace`) > 0) and (`principal_namespace` = trim(`principal_namespace`)))) and " +
				"((`evaluation` = 0) or ((`endpoint` = 'desktop') and (`audience` = 'single') and (`initiation` = 'direct') and (`presentation` = 'chat'))) and " +
				"(`updated_at_ms` >= `created_at_ms`))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "endpoint_conversations_conversation_fk",
			referencedTable: "conversations",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "id", sameSchema: true},
				{name: "character_id", referencedColumn: "character_id", sameSchema: true},
				{name: "kind", referencedColumn: "kind", sameSchema: true},
			},
		}},
	},
	{
		name: "conversation_turns",
		ddl: `CREATE TABLE IF NOT EXISTS conversation_turns (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  message_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  sequence BIGINT NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  origin VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  error_code VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  error_message LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  error_retryable TINYINT UNSIGNED NULL,
  extraction_state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  extraction_claim_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  extraction_lease_owner VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  extraction_lease_expires_at_ms BIGINT UNSIGNED NULL,
  extraction_attempt_count BIGINT UNSIGNED NOT NULL,
  extraction_next_attempt_at_ms BIGINT UNSIGNED NOT NULL,
  extraction_error_code VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  extraction_error_message LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY conversation_turns_conversation_identity_key (conversation_id, id),
  UNIQUE KEY conversation_turns_conversation_sequence_key (conversation_id, sequence),
  KEY conversation_turns_external_message_idx (conversation_id, message_id, sequence),
  KEY conversation_turns_conversation_status_idx (conversation_id, status, sequence),
  KEY conversation_turns_extraction_claim_idx (extraction_claim_id),
  KEY conversation_turns_extraction_queue_idx (
    conversation_id, status, extraction_state, extraction_next_attempt_at_ms, sequence
  ),
  CONSTRAINT conversation_turns_conversation_fk FOREIGN KEY (conversation_id)
    REFERENCES conversations (id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT conversation_turns_invariants_check CHECK (
    CHAR_LENGTH(id) > 0 AND id = TRIM(id) AND
    sequence > 0 AND
    (message_id IS NULL OR
      (CHAR_LENGTH(message_id) BETWEEN 1 AND 128 AND message_id = TRIM(message_id) AND
        message_id NOT REGEXP '[[:cntrl:]]')) AND
    status IN ('interpreting', 'planning', 'responding', 'completed', 'interrupted', 'failed') AND
    origin IN ('user', 'desktop_initiation') AND
    error_retryable IN (0, 1) AND
    extraction_state IN ('ineligible', 'pending', 'claimed', 'processed', 'failed') AND
    ((extraction_state = 'claimed') =
      (extraction_claim_id IS NOT NULL AND extraction_lease_owner IS NOT NULL AND
        extraction_lease_expires_at_ms IS NOT NULL)) AND
    (extraction_state = 'claimed' OR
      (extraction_claim_id IS NULL AND extraction_lease_owner IS NULL AND
        extraction_lease_expires_at_ms IS NULL)) AND
    updated_at_ms >= created_at_ms AND
    ((status = 'failed') = (error_code IS NOT NULL AND error_message IS NOT NULL))
  )
)`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "message_id", columnType: "varchar(128)", nullable: true, collation: "utf8mb4_bin"},
			{name: "sequence", columnType: "bigint"},
			{name: "status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "origin", columnType: "varchar(32)", collation: "ascii_bin"},
			{name: "error_code", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "error_message", columnType: "longtext", nullable: true, collation: "utf8mb4_bin"},
			{name: "error_retryable", columnType: "tinyint unsigned", nullable: true},
			{name: "extraction_state", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "extraction_claim_id", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "extraction_lease_owner", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "extraction_lease_expires_at_ms", columnType: "bigint unsigned", nullable: true},
			{name: "extraction_attempt_count", columnType: "bigint unsigned"},
			{name: "extraction_next_attempt_at_ms", columnType: "bigint unsigned"},
			{name: "extraction_error_code", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "extraction_error_message", columnType: "longtext", nullable: true, collation: "utf8mb4_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex("conversation_turns_conversation_identity_key", true, "conversation_id", "id"),
			ascendingBTreeIndex("conversation_turns_conversation_sequence_key", true, "conversation_id", "sequence"),
			ascendingBTreeIndex("conversation_turns_external_message_idx", false, "conversation_id", "message_id", "sequence"),
			ascendingBTreeIndex("conversation_turns_conversation_status_idx", false, "conversation_id", "status", "sequence"),
			ascendingBTreeIndex("conversation_turns_extraction_claim_idx", false, "extraction_claim_id"),
			ascendingBTreeIndex("conversation_turns_extraction_queue_idx", false, "conversation_id", "status", "extraction_state", "extraction_next_attempt_at_ms", "sequence"),
		},
		checks: []schemaCheck{{
			name: "conversation_turns_invariants_check",
			clause: "((CHAR_LENGTH(`id`) > 0) and (`id` = trim(`id`)) and (`sequence` > 0) and " +
				"((`message_id` is null) or (((CHAR_LENGTH(`message_id`) >= 1) and (CHAR_LENGTH(`message_id`) <= 128)) and (`message_id` = trim(`message_id`)) and (not((`message_id` regexp '[[:cntrl:]]'))))) and " +
				"(`status` in ('interpreting','planning','responding','completed','interrupted','failed')) and " +
				"(`origin` in ('user','desktop_initiation')) and (`error_retryable` in (0,1)) and " +
				"(`extraction_state` in ('ineligible','pending','claimed','processed','failed')) and " +
				"((`extraction_state` = 'claimed') = ((`extraction_claim_id` is not null) and (`extraction_lease_owner` is not null) and (`extraction_lease_expires_at_ms` is not null))) and " +
				"((`extraction_state` = 'claimed') or ((`extraction_claim_id` is null) and (`extraction_lease_owner` is null) and (`extraction_lease_expires_at_ms` is null))) and " +
				"(`updated_at_ms` >= `created_at_ms`) and ((`status` = 'failed') = ((`error_code` is not null) and (`error_message` is not null))))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "conversation_turns_conversation_fk",
			referencedTable: "conversations",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns:         []schemaForeignKeyColumn{{name: "conversation_id", referencedColumn: "id", sameSchema: true}},
		}},
	},
	{
		name: "conversation_messages",
		ddl: `CREATE TABLE IF NOT EXISTS conversation_messages (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  sequence BIGINT NOT NULL,
  role VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  content LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  expression_parts JSON NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY conversation_messages_conversation_sequence_key (conversation_id, sequence),
  UNIQUE KEY conversation_messages_turn_role_key (turn_id, role),
  KEY conversation_messages_conversation_role_created_idx (
    conversation_id, role, created_at_ms, sequence
  ),
  CONSTRAINT conversation_messages_turn_fk FOREIGN KEY (conversation_id, turn_id)
    REFERENCES conversation_turns (conversation_id, id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT conversation_messages_invariants_check CHECK (
    CHAR_LENGTH(id) > 0 AND id = TRIM(id) AND
    sequence > 0 AND
    role IN ('user', 'assistant') AND
    JSON_TYPE(expression_parts) = 'ARRAY' AND
    JSON_LENGTH(expression_parts) <= 12 AND
    (CHAR_LENGTH(content) > 0 OR JSON_LENGTH(expression_parts) > 0)
  )
)`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "sequence", columnType: "bigint"},
			{name: "role", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "content", columnType: "longtext", collation: "utf8mb4_bin"},
			{name: "expression_parts", columnType: "json"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex("conversation_messages_conversation_sequence_key", true, "conversation_id", "sequence"),
			ascendingBTreeIndex("conversation_messages_turn_role_key", true, "turn_id", "role"),
			ascendingBTreeIndex("conversation_messages_conversation_role_created_idx", false, "conversation_id", "role", "created_at_ms", "sequence"),
		},
		checks: []schemaCheck{{
			name: "conversation_messages_invariants_check",
			clause: "((CHAR_LENGTH(`id`) > 0) and (`id` = trim(`id`)) and (`sequence` > 0) and " +
				"(`role` in ('user','assistant')) and (JSON_TYPE(`expression_parts`) = 'ARRAY') and " +
				"(JSON_LENGTH(`expression_parts`) <= 12) and ((CHAR_LENGTH(`content`) > 0) or (JSON_LENGTH(`expression_parts`) > 0)))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "conversation_messages_turn_fk",
			referencedTable: "conversation_turns",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "conversation_id", sameSchema: true},
				{name: "turn_id", referencedColumn: "id", sameSchema: true},
			},
		}},
	},
	{
		name: "prompt_windows",
		ddl: `CREATE TABLE IF NOT EXISTS prompt_windows (
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  revision BIGINT UNSIGNED NOT NULL,
  summary LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  cutoff_message_sequence BIGINT UNSIGNED NOT NULL,
  projection_revision BIGINT UNSIGNED NOT NULL,
  projection_state JSON NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (conversation_id),
  CONSTRAINT prompt_windows_conversation_fk FOREIGN KEY (conversation_id)
    REFERENCES conversations (id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT prompt_windows_invariants_check CHECK (
    revision > 0 AND
    projection_revision > 0 AND
    JSON_TYPE(projection_state) = 'OBJECT'
  )
)`,
		columns: []schemaColumn{
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "revision", columnType: "bigint unsigned"},
			{name: "summary", columnType: "longtext", nullable: true, collation: "utf8mb4_bin"},
			{name: "cutoff_message_sequence", columnType: "bigint unsigned"},
			{name: "projection_revision", columnType: "bigint unsigned"},
			{name: "projection_state", columnType: "json"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{ascendingBTreeIndex("PRIMARY", true, "conversation_id")},
		checks: []schemaCheck{{
			name:   "prompt_windows_invariants_check",
			clause: "((`revision` > 0) and (`projection_revision` > 0) and (JSON_TYPE(`projection_state`) = 'OBJECT'))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "prompt_windows_conversation_fk",
			referencedTable: "conversations",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns:         []schemaForeignKeyColumn{{name: "conversation_id", referencedColumn: "id", sameSchema: true}},
		}},
	},
}

// turnEvidenceSchema is revision 3. Revision 2 is already immutable, so the
// initiation-only evidence edge is added separately instead of rewriting the
// committed conversation schema checksum.
var turnEvidenceSchema = [...]schemaTable{
	{
		name: "conversation_turn_evidence",
		ddl: `CREATE TABLE IF NOT EXISTS conversation_turn_evidence (
  turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  evidence_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (turn_id, evidence_id),
  KEY conversation_turn_evidence_evidence_idx (evidence_id, turn_id),
  CONSTRAINT conversation_turn_evidence_turn_fk FOREIGN KEY (turn_id)
    REFERENCES conversation_turns (id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT conversation_turn_evidence_invariants_check CHECK (
    CHAR_LENGTH(evidence_id) BETWEEN 1 AND 128 AND
    evidence_id = TRIM(evidence_id) AND
    evidence_id NOT REGEXP '[[:cntrl:]]'
  )
)`,
		columns: []schemaColumn{
			{name: "turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "evidence_id", columnType: "varchar(128)", collation: "utf8mb4_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "turn_id", "evidence_id"),
			ascendingBTreeIndex("conversation_turn_evidence_evidence_idx", false, "evidence_id", "turn_id"),
		},
		checks: []schemaCheck{{
			name: "conversation_turn_evidence_invariants_check",
			clause: "(((CHAR_LENGTH(`evidence_id`) >= 1) and (CHAR_LENGTH(`evidence_id`) <= 128)) and " +
				"(`evidence_id` = trim(`evidence_id`)) and (not((`evidence_id` regexp '[[:cntrl:]]'))))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "conversation_turn_evidence_turn_fk",
			referencedTable: "conversation_turns",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns:         []schemaForeignKeyColumn{{name: "turn_id", referencedColumn: "id", sameSchema: true}},
		}},
	},
}

// conversationRuntimeSchema is revision 5. It adds the durable runtime state
// required to continue model lanes, account for every Turn event in order,
// and coordinate context-window compaction without rewriting the immutable
// conversation or transcript-recall revisions.
var conversationRuntimeSchema = [...]schemaTable{
	{
		name: "turn_runtime_events",
		ddl: `CREATE TABLE IF NOT EXISTS turn_runtime_events (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  sequence BIGINT NOT NULL,
  event_type VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  state VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  code VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  metadata_json JSON NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY turn_runtime_events_conversation_turn_sequence_key (conversation_id, turn_id, sequence),
  KEY turn_runtime_events_type_created_idx (event_type, created_at_ms, sequence),
  CONSTRAINT turn_runtime_events_turn_fk FOREIGN KEY (conversation_id, turn_id)
    REFERENCES conversation_turns (conversation_id, id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT turn_runtime_events_invariants_check CHECK (
    CHAR_LENGTH(id) > 0 AND id = TRIM(id) AND
    sequence > 0 AND
    CHAR_LENGTH(event_type) BETWEEN 1 AND 128 AND event_type = TRIM(event_type) AND
      event_type NOT REGEXP '[[:cntrl:]]' AND
    (state IS NULL OR
      (CHAR_LENGTH(state) BETWEEN 1 AND 128 AND state = TRIM(state) AND
        state NOT REGEXP '[[:cntrl:]]')) AND
    (code IS NULL OR
      (CHAR_LENGTH(code) BETWEEN 1 AND 128 AND code = TRIM(code) AND
        code NOT REGEXP '[[:cntrl:]]')) AND
    JSON_TYPE(metadata_json) = 'OBJECT'
  )
)`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "sequence", columnType: "bigint"},
			{name: "event_type", columnType: "varchar(128)", collation: "utf8mb4_bin"},
			{name: "state", columnType: "varchar(128)", nullable: true, collation: "utf8mb4_bin"},
			{name: "code", columnType: "varchar(128)", nullable: true, collation: "utf8mb4_bin"},
			{name: "metadata_json", columnType: "json"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex(
				"turn_runtime_events_conversation_turn_sequence_key", true,
				"conversation_id", "turn_id", "sequence",
			),
			ascendingBTreeIndex(
				"turn_runtime_events_type_created_idx", false,
				"event_type", "created_at_ms", "sequence",
			),
		},
		checks: []schemaCheck{{
			name: "turn_runtime_events_invariants_check",
			clause: "((CHAR_LENGTH(`id`) > 0) and (`id` = trim(`id`)) and (`sequence` > 0) and " +
				"((CHAR_LENGTH(`event_type`) >= 1) and (CHAR_LENGTH(`event_type`) <= 128)) and " +
				"(`event_type` = trim(`event_type`)) and (not((`event_type` regexp '[[:cntrl:]]'))) and " +
				"((`state` is null) or (((CHAR_LENGTH(`state`) >= 1) and (CHAR_LENGTH(`state`) <= 128)) and " +
				"(`state` = trim(`state`)) and (not((`state` regexp '[[:cntrl:]]'))))) and " +
				"((`code` is null) or (((CHAR_LENGTH(`code`) >= 1) and (CHAR_LENGTH(`code`) <= 128)) and " +
				"(`code` = trim(`code`)) and (not((`code` regexp '[[:cntrl:]]'))))) and " +
				"(JSON_TYPE(`metadata_json`) = 'OBJECT'))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "turn_runtime_events_turn_fk",
			referencedTable: "conversation_turns",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "conversation_id", sameSchema: true},
				{name: "turn_id", referencedColumn: "id", sameSchema: true},
			},
		}},
	},
	{
		name: "lane_continuations",
		ddl: `CREATE TABLE IF NOT EXISTS lane_continuations (
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  lane VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  previous_response_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  request_shape_hash VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  input_prefix_hash VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  response_item_hash VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  window_revision BIGINT NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (conversation_id, lane),
  CONSTRAINT lane_continuations_conversation_fk FOREIGN KEY (conversation_id)
    REFERENCES conversations (id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT lane_continuations_invariants_check CHECK (
    lane IN ('respond', 'compact', 'extract') AND
    CHAR_LENGTH(previous_response_id) BETWEEN 1 AND 128 AND
      previous_response_id = TRIM(previous_response_id) AND
      previous_response_id NOT REGEXP '[[:cntrl:]]' AND
    request_shape_hash REGEXP '^[0-9a-f]{64}$' AND
    input_prefix_hash REGEXP '^[0-9a-f]{64}$' AND
    response_item_hash REGEXP '^[0-9a-f]{64}$' AND
    window_revision > 0
  )
)`,
		columns: []schemaColumn{
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "lane", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "previous_response_id", columnType: "varchar(128)", collation: "utf8mb4_bin"},
			{name: "request_shape_hash", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "input_prefix_hash", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "response_item_hash", columnType: "varchar(64)", collation: "ascii_bin"},
			{name: "window_revision", columnType: "bigint"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{ascendingBTreeIndex("PRIMARY", true, "conversation_id", "lane")},
		checks: []schemaCheck{{
			name: "lane_continuations_invariants_check",
			clause: "((`lane` in ('respond','compact','extract')) and " +
				"((CHAR_LENGTH(`previous_response_id`) >= 1) and (CHAR_LENGTH(`previous_response_id`) <= 128)) and " +
				"(`previous_response_id` = trim(`previous_response_id`)) and " +
				"(not((`previous_response_id` regexp '[[:cntrl:]]'))) and " +
				"(`request_shape_hash` regexp '^[0-9a-f]{64}$') and " +
				"(`input_prefix_hash` regexp '^[0-9a-f]{64}$') and " +
				"(`response_item_hash` regexp '^[0-9a-f]{64}$') and (`window_revision` > 0))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "lane_continuations_conversation_fk",
			referencedTable: "conversations",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns:         []schemaForeignKeyColumn{{name: "conversation_id", referencedColumn: "id", sameSchema: true}},
		}},
	},
	{
		name: "context_windows",
		ddl: `CREATE TABLE IF NOT EXISTS context_windows (
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  lane VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  window_number BIGINT NOT NULL,
  first_window_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  previous_window_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  window_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  observed_prefill_tokens BIGINT NULL,
  estimated_prefill_tokens BIGINT NULL,
  last_trigger VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  failure_count BIGINT NOT NULL,
  prompt_window_revision BIGINT NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (conversation_id, lane),
  CONSTRAINT context_windows_conversation_fk FOREIGN KEY (conversation_id)
    REFERENCES conversations (id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT context_windows_invariants_check CHECK (
    lane IN ('respond', 'compact', 'extract') AND
    window_number >= 0 AND
    CHAR_LENGTH(first_window_id) BETWEEN 1 AND 128 AND first_window_id = TRIM(first_window_id) AND
      first_window_id NOT REGEXP '[[:cntrl:]]' AND
    (previous_window_id IS NULL OR
      (CHAR_LENGTH(previous_window_id) BETWEEN 1 AND 128 AND
        previous_window_id = TRIM(previous_window_id) AND
        previous_window_id NOT REGEXP '[[:cntrl:]]')) AND
    CHAR_LENGTH(window_id) BETWEEN 1 AND 128 AND window_id = TRIM(window_id) AND
      window_id NOT REGEXP '[[:cntrl:]]' AND
    (observed_prefill_tokens IS NULL OR observed_prefill_tokens >= 0) AND
    (estimated_prefill_tokens IS NULL OR estimated_prefill_tokens >= 0) AND
    CHAR_LENGTH(last_trigger) BETWEEN 1 AND 128 AND last_trigger = TRIM(last_trigger) AND
      last_trigger NOT REGEXP '[[:cntrl:]]' AND
    failure_count >= 0 AND
    prompt_window_revision > 0
  )
)`,
		columns: []schemaColumn{
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "lane", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "window_number", columnType: "bigint"},
			{name: "first_window_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "previous_window_id", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "window_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "observed_prefill_tokens", columnType: "bigint", nullable: true},
			{name: "estimated_prefill_tokens", columnType: "bigint", nullable: true},
			{name: "last_trigger", columnType: "varchar(128)", collation: "utf8mb4_bin"},
			{name: "failure_count", columnType: "bigint"},
			{name: "prompt_window_revision", columnType: "bigint"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{ascendingBTreeIndex("PRIMARY", true, "conversation_id", "lane")},
		checks: []schemaCheck{{
			name: "context_windows_invariants_check",
			clause: "((`lane` in ('respond','compact','extract')) and (`window_number` >= 0) and " +
				"((CHAR_LENGTH(`first_window_id`) >= 1) and (CHAR_LENGTH(`first_window_id`) <= 128)) and " +
				"(`first_window_id` = trim(`first_window_id`)) and (not((`first_window_id` regexp '[[:cntrl:]]'))) and " +
				"((`previous_window_id` is null) or (((CHAR_LENGTH(`previous_window_id`) >= 1) and " +
				"(CHAR_LENGTH(`previous_window_id`) <= 128)) and (`previous_window_id` = trim(`previous_window_id`)) and " +
				"(not((`previous_window_id` regexp '[[:cntrl:]]'))))) and " +
				"((CHAR_LENGTH(`window_id`) >= 1) and (CHAR_LENGTH(`window_id`) <= 128)) and " +
				"(`window_id` = trim(`window_id`)) and (not((`window_id` regexp '[[:cntrl:]]'))) and " +
				"((`observed_prefill_tokens` is null) or (`observed_prefill_tokens` >= 0)) and " +
				"((`estimated_prefill_tokens` is null) or (`estimated_prefill_tokens` >= 0)) and " +
				"((CHAR_LENGTH(`last_trigger`) >= 1) and (CHAR_LENGTH(`last_trigger`) <= 128)) and " +
				"(`last_trigger` = trim(`last_trigger`)) and (not((`last_trigger` regexp '[[:cntrl:]]'))) and " +
				"(`failure_count` >= 0) and (`prompt_window_revision` > 0))",
		}},
		foreignKeys: []schemaForeignKey{{
			name:            "context_windows_conversation_fk",
			referencedTable: "conversations",
			updateRule:      "restrict",
			deleteRule:      "cascade",
			columns:         []schemaForeignKeyColumn{{name: "conversation_id", referencedColumn: "id", sameSchema: true}},
		}},
	},
}

func foundationSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(foundationSchema))
	for _, table := range foundationSchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func conversationSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(conversationSchema))
	for _, table := range conversationSchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func turnEvidenceSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(turnEvidenceSchema))
	for _, table := range turnEvidenceSchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func transcriptRecallSchemaChecksum() [sha256.Size]byte {
	return schemaDDLChecksum([]string{transcriptRecallIndexDDL})
}

func conversationRuntimeSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(conversationRuntimeSchema))
	for _, table := range conversationRuntimeSchema {
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

func applyConversationSchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range conversationSchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB conversation table %s: %w", table.name, err)
		}
	}
	return nil
}

func verifyConversationSchema(ctx context.Context, connection *sql.Conn) error {
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB CHECK enforcement metadata: %w", err)
	}
	for _, table := range conversationSchema {
		if err := verifySchemaTable(ctx, connection, table, enforcedAvailable); err != nil {
			return err
		}
	}
	return nil
}

func applyTurnEvidenceSchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range turnEvidenceSchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB turn evidence table %s: %w", table.name, err)
		}
	}
	return nil
}

func verifyTurnEvidenceSchema(ctx context.Context, connection *sql.Conn) error {
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB CHECK enforcement metadata: %w", err)
	}
	for _, table := range turnEvidenceSchema {
		if err := verifySchemaTable(ctx, connection, table, enforcedAvailable); err != nil {
			return err
		}
	}
	return nil
}

func applyTranscriptRecallSchema(ctx context.Context, connection *sql.Conn) error {
	if _, err := connection.ExecContext(ctx, transcriptRecallIndexDDL); err != nil {
		return fmt.Errorf("create SeekDB transcript recall index %s: %w", transcriptRecallIndexName, err)
	}
	return nil
}

func applyConversationRuntimeSchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range conversationRuntimeSchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB conversation runtime table %s: %w", table.name, err)
		}
	}
	return nil
}

// verifyTranscriptRecallSchema verifies the cumulative conversation shape at
// revision four. Revision two remains exact and therefore continues to reject
// every extra index when it is verified at its own migration boundary.
func verifyTranscriptRecallSchema(ctx context.Context, connection *sql.Conn) error {
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB CHECK enforcement metadata: %w", err)
	}
	for _, table := range conversationSchema {
		ignoredIndexes := []string(nil)
		if table.name == transcriptRecallTableName {
			ignoredIndexes = []string{transcriptRecallIndexName}
		}
		if err := verifySchemaTableIgnoringIndexes(ctx, connection, table, enforcedAvailable, ignoredIndexes); err != nil {
			return err
		}
	}
	metadata, err := readTranscriptRecallIndexMetadata(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB transcript recall index metadata: %w", err)
	}
	if err := compareTranscriptRecallIndexMetadata(metadata); err != nil {
		return fmt.Errorf("verify SeekDB transcript recall index metadata: %w", err)
	}
	tableName, createTable, err := readTranscriptRecallCreateTable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB transcript recall parser: %w", err)
	}
	if err := compareTranscriptRecallCreateTable(tableName, createTable); err != nil {
		return fmt.Errorf("verify SeekDB transcript recall parser: %w", err)
	}
	return nil
}

// verifyConversationRuntimeSchema is cumulative: revision five remains ready
// only while the exact revision-four conversation shape, logical FULLTEXT
// index, and immutable IK parser contract are still present.
func verifyConversationRuntimeSchema(ctx context.Context, connection *sql.Conn) error {
	if err := verifyTranscriptRecallSchema(ctx, connection); err != nil {
		return err
	}
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB CHECK enforcement metadata: %w", err)
	}
	for _, table := range conversationRuntimeSchema {
		if err := verifySchemaTable(ctx, connection, table, enforcedAvailable); err != nil {
			return err
		}
	}
	return nil
}

func verifySchemaTable(ctx context.Context, connection *sql.Conn, expected schemaTable, checkEnforcementAvailable bool) error {
	return verifySchemaTableIgnoringIndexes(ctx, connection, expected, checkEnforcementAvailable, nil)
}

func verifySchemaTableIgnoringIndexes(
	ctx context.Context,
	connection *sql.Conn,
	expected schemaTable,
	checkEnforcementAvailable bool,
	ignoredIndexNames []string,
) error {
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
	if len(ignoredIndexNames) > 0 {
		indexes = slices.DeleteFunc(indexes, func(index schemaIndex) bool {
			return slices.Contains(ignoredIndexNames, index.name)
		})
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

func readTranscriptRecallIndexMetadata(ctx context.Context, connection *sql.Conn) ([]transcriptRecallIndexMetadata, error) {
	rows, err := connection.QueryContext(ctx, "SHOW INDEX FROM "+transcriptRecallTableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	positions := make(map[string]int, len(columns))
	for index, column := range columns {
		positions[strings.ToLower(column)] = index
	}
	for _, required := range []string{
		"table", "non_unique", "key_name", "seq_in_index", "column_name",
		"collation", "sub_part", "index_type", "comment", "visible",
	} {
		if _, ok := positions[required]; !ok {
			return nil, fmt.Errorf("SHOW INDEX lacks %s column", required)
		}
	}
	values := make([]sql.RawBytes, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	metadata := make([]transcriptRecallIndexMetadata, 0, 1)
	for rows.Next() {
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		if string(values[positions["key_name"]]) != transcriptRecallIndexName {
			continue
		}
		nonUnique, err := parseShowIndexInteger(values[positions["non_unique"]], "non_unique")
		if err != nil {
			return nil, err
		}
		sequence, err := parseShowIndexInteger(values[positions["seq_in_index"]], "seq_in_index")
		if err != nil {
			return nil, err
		}
		subPart, err := parseShowIndexOptionalInt64(values[positions["sub_part"]], "sub_part")
		if err != nil {
			return nil, err
		}
		metadata = append(metadata, transcriptRecallIndexMetadata{
			table:     string(values[positions["table"]]),
			name:      string(values[positions["key_name"]]),
			nonUnique: nonUnique,
			sequence:  sequence,
			column:    string(values[positions["column_name"]]),
			collation: showIndexNullString(values[positions["collation"]]),
			subPart:   subPart,
			indexType: strings.ToLower(string(values[positions["index_type"]])),
			comment:   strings.ToLower(string(values[positions["comment"]])),
			visible:   strings.ToLower(string(values[positions["visible"]])),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return metadata, nil
}

func parseShowIndexInteger(raw sql.RawBytes, column string) (int, error) {
	if raw == nil {
		return 0, fmt.Errorf("SHOW INDEX %s is NULL", column)
	}
	value, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("SHOW INDEX %s %q is not an integer: %w", column, raw, err)
	}
	return value, nil
}

func showIndexNullString(raw sql.RawBytes) sql.NullString {
	if raw == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: strings.ToLower(string(raw)), Valid: true}
}

func parseShowIndexOptionalInt64(raw sql.RawBytes, column string) (sql.NullInt64, error) {
	if raw == nil {
		return sql.NullInt64{}, nil
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return sql.NullInt64{}, fmt.Errorf("SHOW INDEX %s %q is not an integer: %w", column, raw, err)
	}
	return sql.NullInt64{Int64: value, Valid: true}, nil
}

func compareTranscriptRecallIndexMetadata(metadata []transcriptRecallIndexMetadata) error {
	if len(metadata) != 1 {
		return fmt.Errorf("logical index row count = %d, want 1", len(metadata))
	}
	want := transcriptRecallIndexMetadata{
		table:     transcriptRecallTableName,
		name:      transcriptRecallIndexName,
		nonUnique: 1,
		sequence:  1,
		column:    "content",
		collation: sql.NullString{String: "a", Valid: true},
		indexType: "fulltext",
		comment:   "available",
		visible:   "yes",
	}
	if metadata[0] != want {
		return fmt.Errorf("logical index = %#v, want %#v", metadata[0], want)
	}
	return nil
}

func readTranscriptRecallCreateTable(ctx context.Context, connection *sql.Conn) (string, string, error) {
	var tableName, createTable string
	if err := connection.QueryRowContext(ctx, "SHOW CREATE TABLE "+transcriptRecallTableName).Scan(&tableName, &createTable); err != nil {
		return "", "", err
	}
	return tableName, createTable, nil
}

func compareTranscriptRecallCreateTable(tableName, createTable string) error {
	if tableName != transcriptRecallTableName {
		return fmt.Errorf("SHOW CREATE TABLE name = %q, want %q", tableName, transcriptRecallTableName)
	}
	normalized := strings.ToLower(normalizeDDL(createTable))
	want := "fulltext key `conversation_messages_content_fts_idx` (`content`) " +
		"with parser ik parser_properties=(ik_mode=\"max_word\")"
	if !strings.Contains(normalized, want) {
		return fmt.Errorf("SHOW CREATE TABLE lacks immutable parser clause %q", want)
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
